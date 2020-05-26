/*
Copyright 2020 Gravitational, Inc.

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

package validate

import (
	"github.com/gravitational/gravity/lib/storage/clusterconfig"

	"github.com/gravitational/trace"
)

// ClusterConfiguration validates that `update` can update `existing` without invalidating consistency.
func ClusterConfiguration(existing, update clusterconfig.Interface) error {
	if newGlobalConfig := update.GetGlobalConfig(); !isCloudConfigEmpty(newGlobalConfig) {
		// TODO(dmitri): require cloud provider if cloud-config is being updated
		// This is more a sanity check than a hard requirement so users are explicit about changes
		// in the cloud configuration
		if newGlobalConfig.CloudConfig != "" && newGlobalConfig.CloudProvider == "" {
			return trace.BadParameter("cloud provider is required when updating cloud configuration")
		}
	}
	newGlobalConfig := update.GetGlobalConfig()
	if newGlobalConfig.IsEmpty() {
		return trace.BadParameter("provided cluster configuration is empty")
	}
	globalConfig := existing.GetGlobalConfig()
	if isCloudConfigEmpty(globalConfig) {
		if newGlobalConfig := update.GetGlobalConfig(); !isCloudConfigEmpty(newGlobalConfig) {
			return trace.BadParameter("cannot change cloud configuration: cluster does not have cloud provider configured")
		}
	}
	if newGlobalConfig.CloudProvider != "" && globalConfig.CloudProvider != newGlobalConfig.CloudProvider {
		return trace.BadParameter("changing cloud provider is not supported (%q -> %q)",
			newGlobalConfig.CloudProvider, globalConfig.CloudProvider)
	}
	if globalConfig.CloudProvider == "" && newGlobalConfig.CloudConfig != "" {
		return trace.BadParameter("cannot set cloud configuration: cluster does not have cloud provider configured")
	}
	if newGlobalConfig.PodCIDR == globalConfig.PodCIDR {
		return trace.BadParameter("specified pod subnet (%v) is the same as existing pod subnet",
			newGlobalConfig.PodCIDR)
	}
	if newGlobalConfig.ServiceCIDR == globalConfig.ServiceCIDR {
		return trace.BadParameter("specified service subnet (%v) is the same as existing service subnet",
			newGlobalConfig.ServiceCIDR)
	}
	if newGlobalConfig.PodCIDR != "" {
		serviceCIDR := newGlobalConfig.ServiceCIDR
		if serviceCIDR == "" {
			serviceCIDR = globalConfig.ServiceCIDR
		}
		if err := KubernetesSubnets(newGlobalConfig.PodCIDR, serviceCIDR); err != nil {
			return trace.Wrap(err)
		}
	}
	if newGlobalConfig.ServiceCIDR != "" {
		podCIDR := newGlobalConfig.PodCIDR
		if podCIDR == "" {
			podCIDR = globalConfig.PodCIDR
		}
		if err := KubernetesSubnets(podCIDR, newGlobalConfig.ServiceCIDR); err != nil {
			return trace.Wrap(err)
		}
	}
	return nil
}

func isCloudConfigEmpty(global clusterconfig.Global) bool {
	return global.CloudProvider == "" && global.CloudConfig == ""
}
