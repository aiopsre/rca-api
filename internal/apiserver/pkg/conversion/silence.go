package conversion

import (
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/silenceutil"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

// SilenceMToSilenceV1 converts model silence to API silence.
func SilenceMToSilenceV1(m *model.SilenceM) *v1.Silence {
	if m == nil {
		return nil
	}

	matchers, _ := silenceutil.DecodeMatchers(m.MatchersJSON)
	return &v1.Silence{
		SilenceID: m.SilenceID,
		Namespace: m.Namespace,
		Enabled:   m.Enabled,
		StartsAt:  timestamppb.New(m.StartsAt.UTC()),
		EndsAt:    timestamppb.New(m.EndsAt.UTC()),
		Reason:    cloneOptionalString(m.Reason),
		CreatedBy: cloneOptionalString(m.CreatedBy),
		Matchers:  SilenceMatchersToV1(matchers),
		CreatedAt: timestamppb.New(m.CreatedAt.UTC()),
		UpdatedAt: timestamppb.New(m.UpdatedAt.UTC()),
	}
}

// SilenceMatchersToV1 converts persisted matcher list to API matcher list.
func SilenceMatchersToV1(matchers []silenceutil.Matcher) []*v1.SilenceMatcher {
	out := make([]*v1.SilenceMatcher, 0, len(matchers))
	for _, m := range matchers {
		out = append(out, &v1.SilenceMatcher{
			Key:   m.Key,
			Op:    m.Op,
			Value: m.Value,
		})
	}
	return out
}

// SilenceMatchersFromV1 converts API matcher list to persisted matcher list.
func SilenceMatchersFromV1(matchers []*v1.SilenceMatcher) []silenceutil.Matcher {
	out := make([]silenceutil.Matcher, 0, len(matchers))
	for _, m := range matchers {
		if m == nil {
			continue
		}
		out = append(out, silenceutil.Matcher{
			Key:   strings.TrimSpace(m.GetKey()),
			Op:    strings.TrimSpace(m.GetOp()),
			Value: strings.TrimSpace(m.GetValue()),
		})
	}
	return out
}

func cloneOptionalString(v *string) *string {
	if v == nil {
		return nil
	}
	c := strings.TrimSpace(*v)
	return &c
}
