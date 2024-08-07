# Changelog

## 2.0.1 - 2024-08-07

1. The client will fall back to the `/decide` endpoint when evaluating feature flags if the user does not wish to provide a PersonalApiKey.  This fixes an issue where users were unable to use this SDK without providing a PersonalApiKey.  This fallback will make feature flag usage less performant, but will save users money by not making them pay for public API access.

## 2.0.0 - 2022-08-15

Breaking changes:

1. Minimum PostHog version requirement: 1.38
2. Local Evaluation added to IsFeatureEnabled and GetFeatureFlag. These functions now accept person and group properties arguments. The arguments will be used to locally evaluate relevant feature flags.
3. Feature flag functions take a payload called `FeatureFlagPayload` when a key is require and `FeatureFlagPayloadNoKey` when a key is not required. The payload will handle defaults for all unspecified arguments automatically.
3. Feature Flag defaults have been removed. If the flag fails for any reason, nil will be returned.
4. GetAllFlags argument added. This function returns all flags related to the id.