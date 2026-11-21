package llm

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
)

// ProfileGetter is the one Bedrock control-plane call the residency check needs.
type ProfileGetter interface {
	GetInferenceProfile(ctx context.Context, in *bedrock.GetInferenceProfileInput, optFns ...func(*bedrock.Options)) (*bedrock.GetInferenceProfileOutput, error)
}

// arnRegion returns the region field of an ARN (arn:aws:bedrock:<region>:...).
func arnRegion(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

// VerifyProfileRouting checks, as AWS advises, that each inference profile can
// only route to the allowed regions, instead of trusting the profile ID prefix.
// Any profile that could route elsewhere stops the worker from starting.
func VerifyProfileRouting(ctx context.Context, client ProfileGetter, profiles []string, allowed []string) error {
	seen := map[string]bool{}
	for _, id := range profiles {
		if seen[id] {
			continue
		}
		seen[id] = true
		out, err := client.GetInferenceProfile(ctx, &bedrock.GetInferenceProfileInput{InferenceProfileIdentifier: aws.String(id)})
		if err != nil {
			return fmt.Errorf("inference profile %s: %w", id, err)
		}
		if len(out.Models) == 0 {
			return fmt.Errorf("inference profile %s lists no models; cannot verify where it routes", id)
		}
		for _, m := range out.Models {
			r := arnRegion(aws.ToString(m.ModelArn))
			if !slices.Contains(allowed, r) {
				return fmt.Errorf("inference profile %s can route to %q, which is not in allowed_regions %v", id, r, allowed)
			}
		}
	}
	return nil
}
