/*
Copyright 2026 The Kubernetes Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
)

func Test_ValidationError_Error(t *testing.T) {
	tt := []struct {
		name    string
		err     *ValidationError
		wantMsg string
	}{
		{
			name:    "returns message field",
			err:     &ValidationError{Reason: "Invalid", Message: "port must be positive"},
			wantMsg: "port must be positive",
		},
		{
			name:    "empty message",
			err:     &ValidationError{Reason: "Invalid", Message: ""},
			wantMsg: "",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			if got != tc.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

// Test_validateNetworkPolicyPeer_FieldPathFormatting confirms that list-field
// peer validation (validateNetworkPolicyPeer) keeps the indexed "field[N]"
// format, while singular-field validation (validateNetworkPolicyPeerAtPath,
// used for MCPServer.Spec.Network.DNSEgressPeer) reports the bare field path
// without an index, since there is no array to index into.
func Test_validateNetworkPolicyPeer_FieldPathFormatting(t *testing.T) {
	emptyPeer := networkingv1.NetworkPolicyPeer{}

	t.Run("list field keeps indexed format", func(t *testing.T) {
		err := validateNetworkPolicyPeer(emptyPeer, "network.egressTo", 0)
		if err == nil {
			t.Fatal("expected a ValidationError, got nil")
		}
		if !strings.HasPrefix(err.Message, "network.egressTo[0]:") {
			t.Errorf("Message = %q, want prefix %q", err.Message, "network.egressTo[0]:")
		}
	})

	t.Run("singular field omits index", func(t *testing.T) {
		err := validateNetworkPolicyPeerAtPath(emptyPeer, "network.dnsEgressPeer")
		if err == nil {
			t.Fatal("expected a ValidationError, got nil")
		}
		if !strings.HasPrefix(err.Message, "network.dnsEgressPeer:") {
			t.Errorf("Message = %q, want prefix %q", err.Message, "network.dnsEgressPeer:")
		}
		if strings.Contains(err.Message, "dnsEgressPeer[") {
			t.Errorf("Message = %q, must not contain an index suffix", err.Message)
		}
	})

	t.Run("singular field keeps nested except index", func(t *testing.T) {
		peer := networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{
				CIDR:   "10.0.0.0/8",
				Except: []string{"not-a-cidr"},
			},
		}
		err := validateNetworkPolicyPeerAtPath(peer, "network.dnsEgressPeer")
		if err == nil {
			t.Fatal("expected a ValidationError, got nil")
		}
		if !strings.HasPrefix(err.Message, "network.dnsEgressPeer: invalid ipBlock.except[0]") {
			t.Errorf("Message = %q, want prefix %q", err.Message, "network.dnsEgressPeer: invalid ipBlock.except[0]")
		}
	})
}
