package evaluations_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/posthog/posthog-go/v2/ai/evaluations"
)

// Pin the dataset and scorer versions before executing cases. The caller
// supplies the application and evaluator; each acknowledged result links back
// to the exact dataset item version that produced it. Grant the project secret
// key dataset:read, llm_analytics:read, and offline_evaluation_ingestion:write.
func ExampleClient() {
	client, err := evaluations.NewClient(evaluations.Config{
		ProjectID: 42,
		SecretKey: os.Getenv("POSTHOG_PROJECT_SECRET_API_KEY"),
	})
	if err != nil {
		log.Print(err)
		return
	}
	defer client.Close()

	ctx := context.Background()
	snapshot, err := client.Datasets.Snapshot(ctx, os.Getenv("DATASET_ID"), nil)
	if err != nil {
		log.Print(err)
		return
	}
	scorer, err := client.Scorers.Get(ctx, os.Getenv("BOOLEAN_SCORER_ID"))
	if err != nil {
		log.Print(err)
		return
	}
	run, err := client.CreateExperiment(ctx, evaluations.Fields{
		"name": "Text normalization", "dataset_revision_id": snapshot.RevisionID(),
	})
	if err != nil {
		log.Print(err)
		return
	}
	err = snapshot.ForEach(ctx, 25, func(row evaluations.DatasetItem) error {
		input, inputOK := row.Input.(string)
		expected, expectedOK := row.ExpectedOutput.(string)
		if !inputOK || !expectedOK {
			return fmt.Errorf("dataset item %s requires text input and expected output", row.ID)
		}
		output := strings.TrimSpace(input)
		item, err := evaluations.ItemFromDataset(row, evaluations.Fields{"output": output})
		if err != nil {
			return err
		}
		result, err := evaluations.NewResult(item, scorer.CurrentVersionID, evaluations.Fields{"value": output == expected})
		if err != nil {
			return err
		}
		_, err = run.UploadResult(ctx, result)
		return err
	})
	if err != nil {
		log.Printf("Experiment %s is still open: %v", run.ID(), err)
		return
	}
	if _, err := run.Complete(ctx); err != nil {
		log.Print(err)
	}
}
