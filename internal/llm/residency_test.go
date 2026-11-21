package llm

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrock/types"
)

type fakeProfiles map[string][]string

func (f fakeProfiles) GetInferenceProfile(_ context.Context, in *bedrock.GetInferenceProfileInput, _ ...func(*bedrock.Options)) (*bedrock.GetInferenceProfileOutput, error) {
	var models []types.InferenceProfileModel
	for _, arn := range f[aws.ToString(in.InferenceProfileIdentifier)] {
		models = append(models, types.InferenceProfileModel{ModelArn: aws.String(arn)})
	}
	return &bedrock.GetInferenceProfileOutput{Models: models}, nil
}

func TestVerifyProfileRouting(t *testing.T) {
	allowed := []string{"region-in-1", "region-in-2"}
	f := fakeProfiles{
		"in.model":     {"arn:aws:bedrock:region-in-1::foundation-model/m", "arn:aws:bedrock:region-in-2::foundation-model/m"},
		"global.model": {"arn:aws:bedrock:region-in-1::foundation-model/m", "arn:aws:bedrock:region-us-1::foundation-model/m"},
		"empty.model":  {},
	}
	if err := VerifyProfileRouting(context.Background(), f, []string{"in.model"}, allowed); err != nil {
		t.Fatalf("in-country profile rejected: %v", err)
	}
	if err := VerifyProfileRouting(context.Background(), f, []string{"global.model"}, allowed); err == nil {
		t.Fatal("profile routing abroad was accepted")
	}
	if err := VerifyProfileRouting(context.Background(), f, []string{"empty.model"}, allowed); err == nil {
		t.Fatal("unverifiable profile was accepted")
	}
}
