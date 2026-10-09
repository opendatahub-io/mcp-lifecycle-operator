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

package providers

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Factory creates and registers a provider controller with the manager.
type Factory func(mgr ctrl.Manager) error

// Registration holds a provider factory and its CRD dependencies.
type Registration struct {
	Factory      Factory
	RequiredCRDs []schema.GroupVersionKind
}

var registry []namedRegistration

type namedRegistration struct {
	name string
	reg  Registration
}

// Register adds a provider to the global registry.
// Providers call this from their init() function.
func Register(name string, reg Registration) {
	registry = append(registry, namedRegistration{name: name, reg: reg})
}

// SetupAll starts providers whose CRDs are already available and sets up a
// CRD watcher to dynamically start the remaining providers when their
// required CRDs appear.
func SetupAll(mgr ctrl.Manager) error {
	watcher := &crdWatcher{
		mgr:     mgr,
		started: make(map[string]bool),
	}

	for _, nf := range registry {
		if len(nf.reg.RequiredCRDs) == 0 {
			if err := nf.reg.Factory(mgr); err != nil {
				return fmt.Errorf("provider %s: %w", nf.name, err)
			}
			watcher.started[nf.name] = true
			continue
		}

		log := mgr.GetLogger().WithName("setup")
		missing, err := missingCRDs(mgr, nf.reg.RequiredCRDs)
		if err != nil {
			log.Info("Provider CRD discovery failed, deferring to CRD watcher",
				"provider", nf.name, "error", err)
			watcher.pending = append(watcher.pending, nf)
			continue
		}
		if len(missing) > 0 {
			log.Info("Provider CRDs not yet available, deferring to CRD watcher",
				"provider", nf.name, "missing", missing)
			watcher.pending = append(watcher.pending, nf)
			continue
		}

		if err := nf.reg.Factory(mgr); err != nil {
			return fmt.Errorf("provider %s: %w", nf.name, err)
		}
		watcher.started[nf.name] = true
	}

	if len(watcher.pending) > 0 {
		return watcher.setupWithManager(mgr)
	}
	return nil
}
