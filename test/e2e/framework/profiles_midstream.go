package framework

import (
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/category"
	"github.com/kubernetes-sigs/mcp-lifecycle-operator/test/e2e/framework/labels/speed"
)

func init() {
	RegisterProfile(Profile{
		Name:     "sanity",
		Priority: 1,
		Labels: map[string][]string{
			category.Label: {category.Lifecycle},
			speed.Label:    {speed.Fast},
		},
	})
	RegisterProfile(Profile{
		Name:     "tier1",
		Priority: 1,
		Labels: map[string][]string{
			speed.Label: {speed.Fast},
		},
	})
	RegisterProfile(Profile{
		Name:     "tier2",
		Priority: 1,
		Labels: map[string][]string{
			speed.Label: {speed.Moderate},
		},
	})
	RegisterProfile(Profile{
		Name:     "tier3",
		Priority: 1,
		Labels: map[string][]string{
			speed.Label: {speed.Slow},
		},
	})
	RegisterProfile(Profile{
		Name:     "pre-upgrade",
		Priority: 1,
		Labels: map[string][]string{
			speed.Label: {speed.Fast, speed.Moderate},
		},
	})
	RegisterProfile(Profile{
		Name:     "post-upgrade",
		Priority: 1,
		Labels: map[string][]string{
			speed.Label: {speed.Fast, speed.Moderate},
		},
	})
}
