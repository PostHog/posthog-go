package posthog

import (
	"math"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func floatPtr(v float64) *float64 { return &v }

func stringPtr(v string) *string { return &v }

// holdoutFlag builds an active multivariate flag whose only release condition
// matches everyone and forces the "control" variant, so any result other than
// "control" or false comes from the holdout.
func holdoutFlag(holdout *Holdout) FeatureFlag {
	return FeatureFlag{
		Key:    "checkout",
		Active: true,
		Filters: Filter{
			Groups: []FeatureFlagCondition{{
				RolloutPercentage: floatPtr(100),
				Variant:           stringPtr("control"),
			}},
			Multivariate: &Variants{Variants: []FlagVariant{
				{Key: "control", RolloutPercentage: floatPtr(50)},
				{Key: "test", RolloutPercentage: floatPtr(50)},
			}},
			Holdout: holdout,
		},
	}
}

func evaluateLocally(t *testing.T, poller *FeatureFlagsPoller, flag FeatureFlag, distinctId string) interface{} {
	t.Helper()
	value, err := poller.computeFlagLocally(flag, distinctId, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	return value
}

func TestHoldoutHashMatchesServer(t *testing.T) {
	// The server hashes `holdout-<value>`; the dot-separated flag hash selects a
	// different population, so these vectors pin the separator.
	for _, tt := range []struct {
		bucketingValue string
		want           float64
	}{
		{"user-1", 0.17805599206573022},
		{"user-5", 0.6563813925994418},
	} {
		t.Run(tt.bucketingValue, func(t *testing.T) {
			got := holdoutHash(tt.bucketingValue)
			if math.Abs(got-tt.want) > 0.000000001 {
				t.Fatalf("holdoutHash(%q) = %.17f, want %.17f", tt.bucketingValue, got, tt.want)
			}
		})
	}
}

func TestHoldoutWinsOverTargetingAndVariantOverride(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	flag := holdoutFlag(&Holdout{ID: float64(727), ExclusionPercentage: floatPtr(100)})
	// Release conditions that would otherwise exclude the user entirely.
	flag.Filters.Groups = []FeatureFlagCondition{{
		Properties: []FlagProperty{
			{Key: "region", Operator: "exact", Value: "USA", Type: "person"},
		},
		RolloutPercentage: floatPtr(0),
		Variant:           stringPtr("test"),
	}}

	require.Equal(t, "holdout-727", evaluateLocally(t, poller, flag, "user-1"))
}

func TestHoldoutDoesNotEnableInactiveFlag(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	flag := holdoutFlag(&Holdout{ID: float64(727), ExclusionPercentage: floatPtr(100)})
	flag.Active = false

	require.Equal(t, false, evaluateLocally(t, poller, flag, "user-1"))
}

func TestNoHoldoutPreservesOrdinaryAssignment(t *testing.T) {
	poller := &FeatureFlagsPoller{}

	require.Equal(t, "control", evaluateLocally(t, poller, holdoutFlag(nil), "user-1"))
}

func TestIncompleteHoldoutPreservesOrdinaryAssignment(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	for name, holdout := range map[string]*Holdout{
		"missing id":                   {ExclusionPercentage: floatPtr(100)},
		"missing exclusion percentage": {ID: float64(727)},
		"both missing":                 {},
		"unusable id":                  {ID: []interface{}{727}, ExclusionPercentage: floatPtr(100)},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, "control", evaluateLocally(t, poller, holdoutFlag(holdout), "user-1"))
		})
	}
}

func TestHoldoutPartialMembershipMatchesServer(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	flag := holdoutFlag(&Holdout{ID: float64(727), ExclusionPercentage: floatPtr(20)})

	require.Equal(t, "holdout-727", evaluateLocally(t, poller, flag, "user-1"))
	require.Equal(t, "control", evaluateLocally(t, poller, flag, "user-5"))
}

func TestHoldoutPercentageBoundariesAreInclusiveAndClamped(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	// holdoutHash("user-1") is 0.17805599206573022.
	for _, tt := range []struct {
		percentage float64
		want       interface{}
	}{
		{17.805599206573023, "holdout-727"},
		{17.8, "control"},
		{0, "control"},
		{-10, "control"},
		{100, "holdout-727"},
		{150, "holdout-727"},
	} {
		flag := holdoutFlag(&Holdout{ID: float64(727), ExclusionPercentage: floatPtr(tt.percentage)})
		require.Equal(t, tt.want, evaluateLocally(t, poller, flag, "user-1"), "percentage %v", tt.percentage)
	}
}

func TestHoldoutUsesFlagLevelGroupIdentity(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	groupTypeIndex := uint8(0)
	flag := holdoutFlag(&Holdout{ID: float64(727), ExclusionPercentage: floatPtr(20)})
	flag.Filters.AggregationGroupTypeIndex = &groupTypeIndex

	state := &flagsState{
		flagsByKey: map[string]FeatureFlag{flag.Key: flag},
		groups:     map[string]string{"0": "organization"},
	}

	// The group key is held out even though the distinct id is not.
	value, err := poller.computeFlagLocally(
		flag, "user-5", nil, Groups{"organization": "user-1"}, nil, nil, nil, state,
	)
	require.NoError(t, err)
	require.Equal(t, "holdout-727", value)
}

func TestHoldoutUsesDeviceIdentityForDeviceBucketedFlags(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	flag := holdoutFlag(&Holdout{ID: float64(727), ExclusionPercentage: floatPtr(20)})
	flag.BucketingIdentifier = stringPtr(bucketingIdentifierDevice)

	// The device id is held out even though the distinct id is not.
	value, err := poller.computeFlagLocally(flag, "user-5", stringPtr("user-1"), nil, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "holdout-727", value)
}

func TestHoldoutValueIsVisibleToDependentFlags(t *testing.T) {
	poller := &FeatureFlagsPoller{}
	checkout := holdoutFlag(&Holdout{ID: float64(727), ExclusionPercentage: floatPtr(100)})
	dependent := FeatureFlag{
		Key:    "checkout-banner",
		Active: true,
		Filters: Filter{
			Groups: []FeatureFlagCondition{{
				Properties: []FlagProperty{{
					Key:             "checkout",
					Operator:        "flag_evaluates_to",
					Value:           "holdout-727",
					Type:            "flag",
					DependencyChain: []string{"checkout"},
				}},
				RolloutPercentage: floatPtr(100),
			}},
		},
	}

	state := &flagsState{
		flagsByKey: map[string]FeatureFlag{"checkout": checkout, "checkout-banner": dependent},
		groups:     map[string]string{},
	}

	checkoutValue, err := poller.computeFlagLocally(checkout, "user-1", nil, nil, nil, nil, nil, state)
	require.NoError(t, err)
	require.Equal(t, "holdout-727", checkoutValue)

	dependentValue, err := poller.computeFlagLocally(dependent, "user-1", nil, nil, nil, nil, nil, state)
	require.NoError(t, err)
	require.Equal(t, true, dependentValue)
}

func TestHoldoutSurvivesDefinitionDecoding(t *testing.T) {
	var flag FeatureFlag
	require.NoError(t, json.Unmarshal([]byte(`{
		"key": "checkout",
		"active": true,
		"filters": {"holdout": {"id": 727, "exclusion_percentage": 12.5}}
	}`), &flag))

	require.NotNil(t, flag.Filters.Holdout)
	require.Equal(t, float64(727), flag.Filters.Holdout.ID)
	require.Equal(t, 12.5, *flag.Filters.Holdout.ExclusionPercentage)

	// A refreshed definition that omits the holdout drops it rather than
	// retaining the previous one.
	var refreshed FeatureFlag
	require.NoError(t, json.Unmarshal([]byte(`{
		"key": "checkout",
		"active": true,
		"filters": {}
	}`), &refreshed))
	require.Nil(t, refreshed.Filters.Holdout)
}

func TestHoldoutIDRendering(t *testing.T) {
	for name, id := range map[string]interface{}{
		"number": float64(727),
		"string": "727",
	} {
		t.Run(name, func(t *testing.T) {
			flag := holdoutFlag(&Holdout{ID: id, ExclusionPercentage: floatPtr(100)})
			variant, held := getHoldoutVariant(flag, "user-1")
			require.True(t, held)
			require.Equal(t, "holdout-727", variant)
		})
	}
}
