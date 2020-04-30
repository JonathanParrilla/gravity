/*
Copyright 2018 Gravitational, Inc.

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

package phases

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gravitational/gravity/lib/app"
	"github.com/gravitational/gravity/lib/checks"
	"github.com/gravitational/gravity/lib/constants"
	"github.com/gravitational/gravity/lib/defaults"
	"github.com/gravitational/gravity/lib/fsm"
	"github.com/gravitational/gravity/lib/install"
	"github.com/gravitational/gravity/lib/loc"
	"github.com/gravitational/gravity/lib/ops"
	"github.com/gravitational/gravity/lib/pack"
	"github.com/gravitational/gravity/lib/rpc"
	"github.com/gravitational/gravity/lib/schema"
	"github.com/gravitational/gravity/lib/state"
	"github.com/gravitational/gravity/lib/storage"
	"github.com/gravitational/gravity/lib/update"
	"github.com/gravitational/gravity/lib/users"
	"github.com/gravitational/gravity/lib/utils"

	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"
)

// updatePhaseInit is the update init phase which performs the following:
//   - verifies that the admin agent user exists
//   - updates the cluster with service user details
//   - cleans up state left from previous versions
type updatePhaseInit struct {
	// Backend is the cluster etcd backend
	Backend storage.Backend
	// LocalBackend is the local state backend
	LocalBackend storage.Backend
	// Operator is the local cluster ops service
	Operator ops.Operator
	// Packages is the cluster package service
	Packages pack.PackageService
	// Users is the cluster users service
	Users users.Identity
	// Cluster is the local cluster
	Cluster ops.Site
	// Operation is the current update operation
	Operation ops.SiteOperation
	// Servers is the list of local cluster servers
	Servers []storage.Server
	// FieldLogger is used for logging
	log.FieldLogger
	// updateManifest specifies the manifest of the update application
	updateManifest schema.Manifest
	// installedApp references the installed application instance
	installedApp app.Application
	// existingDocker describes the existing Docker configuration
	existingDocker storage.DockerConfig
	// existingDNS is the existing DNS configuration
	existingDNS storage.DNSConfig
	// init specifies the optional server-specific initialization
	init          *updatePhaseInitServer
	existingPeers []string
}

// NewUpdatePhaseInitLeader creates a new update init phase executor
func NewUpdatePhaseInitLeader(
	p fsm.ExecutorParams,
	operator ops.Operator,
	apps app.Applications,
	backend, localBackend storage.Backend,
	packages, localPackages pack.PackageService,
	users users.Identity,
	logger log.FieldLogger,
) (*updatePhaseInit, error) {
	if p.Phase.Data == nil || p.Phase.Data.Package == nil {
		return nil, trace.BadParameter("no application package specified for phase %v", p.Phase)
	}
	if p.Phase.Data.InstalledPackage == nil {
		return nil, trace.BadParameter("no installed application package specified for phase %v", p.Phase)
	}
	cluster, err := operator.GetLocalSite()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	operation, err := operator.GetSiteOperation(fsm.OperationKey(p.Plan))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	installOperation, err := ops.GetCompletedInstallOperation(cluster.Key(), operator)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	app, err := apps.GetApp(*p.Phase.Data.Package)
	if err != nil {
		return nil, trace.Wrap(err, "failed to query application")
	}
	installedApp, err := apps.GetApp(*p.Phase.Data.InstalledPackage)
	if err != nil {
		return nil, trace.Wrap(err, "failed to query installed application")
	}

	existingDocker := checks.DockerConfigFromSchemaValue(installedApp.Manifest.SystemDocker())
	checks.OverrideDockerConfig(&existingDocker, installOperation.InstallExpand.Vars.System.Docker)

	var init *updatePhaseInitServer
	if p.Phase.Data.Update != nil && len(p.Phase.Data.Update.Servers) != 0 {
		init = &updatePhaseInitServer{
			FieldLogger:   logger,
			localPackages: localPackages,
			clusterName:   cluster.Domain,
			server:        p.Phase.Data.Update.Servers[0],
		}
	}

	return &updatePhaseInit{
		Backend:        backend,
		LocalBackend:   localBackend,
		Operator:       operator,
		Packages:       packages,
		Users:          users,
		Cluster:        *cluster,
		Operation:      *operation,
		Servers:        p.Plan.Servers,
		FieldLogger:    logger,
		updateManifest: app.Manifest,
		installedApp:   *installedApp,
		existingDocker: existingDocker,
		existingDNS:    p.Plan.DNSConfig,
		init:           init,
		existingPeers:  clusterPeers(p.Phase.Data.Update.Servers),
	}, nil
}

// PreCheck is a no-op for this phase
func (p *updatePhaseInit) PreCheck(context.Context) error {
	return nil
}

// PostCheck is no-op for the init phase
func (p *updatePhaseInit) PostCheck(context.Context) error {
	return nil
}

// Execute prepares the update.
func (p *updatePhaseInit) Execute(ctx context.Context) error {
	err := removeLegacyUpdateDirectory(p.FieldLogger)
	if err != nil {
		p.WithError(err).Warn("Failed to remove legacy update directory.")
	}
	if err := p.createAdminAgent(); err != nil {
		return trace.Wrap(err, "failed to create cluster admin agent")
	}
	if err := p.upsertServiceUser(); err != nil {
		return trace.Wrap(err, "failed to upsert service user")
	}
	if err := p.initRPCCredentials(); err != nil {
		return trace.Wrap(err, "failed to init RPC credentials")
	}
	if err := p.updateClusterRoles(); err != nil {
		return trace.Wrap(err, "failed to update RPC credentials")
	}
	if err := p.updateClusterDNSConfig(); err != nil {
		return trace.Wrap(err, "failed to update DNS configuration")
	}
	if err := p.updateDockerConfig(); err != nil {
		return trace.Wrap(err, "failed to update Docker configuration")
	}
	if err := p.removeInvalidObjectPeers(); err != nil {
		return trace.Wrap(err, "failed to clean up object peers")
	}
	if p.init != nil {
		if err := p.init.Execute(ctx); err != nil {
			return trace.Wrap(err)
		}
	}
	return nil
}

// Rollback rolls back the init phase
func (p *updatePhaseInit) Rollback(ctx context.Context) error {
	if p.init != nil {
		if err := p.init.Rollback(ctx); err != nil {
			return trace.Wrap(err)
		}
	}
	if err := p.removeConfiguredPackages(); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

func (p *updatePhaseInit) initRPCCredentials() error {
	// FIXME: the secrets package is currently only generated once.
	// Even though the package is generated with some time buffer in advance,
	// we need to make sure if the existing package needs to be rotated (i.e.
	// as expiring soon).
	// This will ether need to generate a new package version and then the
	// problem becomes how the agents will know the name of the package.
	// Or, the package version is recycled and then we need to make sure
	// to restart the cluster controller (gravity-site) to make sure it has
	// reloaded its copy of the credentials.
	// See: https://github.com/gravitational/gravity/issues/3607.
	pkg, err := rpc.InitCredentials(p.Packages)
	if err != nil && !trace.IsAlreadyExists(err) {
		return trace.Wrap(err)
	}

	if trace.IsAlreadyExists(err) {
		p.Info("RPC credentials already initialized.")
	} else {
		p.Infof("Initialized RPC credentials: %v.", pkg)
	}

	return nil
}

func (p *updatePhaseInit) updateClusterRoles() error {
	p.Info("Update cluster roles.")
	cluster, err := p.Backend.GetLocalSite(defaults.SystemAccountID)
	if err != nil {
		return trace.Wrap(err)
	}

	state := make(map[string]storage.Server, len(p.Servers))
	for _, server := range p.Servers {
		state[server.AdvertiseIP] = server
	}

	for i, server := range cluster.ClusterState.Servers {
		if server.ClusterRole != "" {
			continue
		}
		stateServer := state[server.AdvertiseIP]
		cluster.ClusterState.Servers[i].ClusterRole = stateServer.ClusterRole
	}

	_, err = p.Backend.UpdateSite(*cluster)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

func (p *updatePhaseInit) updateClusterDNSConfig() error {
	p.Info("Update cluster DNS configuration.")
	cluster, err := p.Backend.GetLocalSite(defaults.SystemAccountID)
	if err != nil {
		return trace.Wrap(err)
	}

	if cluster.DNSConfig.IsEmpty() {
		cluster.DNSConfig = p.existingDNS
	}

	_, err = p.Backend.UpdateSite(*cluster)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// updateDockerConfig persists the Docker configuration
// of the currently installed application
func (p *updatePhaseInit) updateDockerConfig() error {
	cluster, err := p.Backend.GetLocalSite(defaults.SystemAccountID)
	if err != nil {
		return trace.Wrap(err)
	}

	if !cluster.ClusterState.Docker.IsEmpty() {
		// Nothing to do
		return nil
	}

	cluster.ClusterState.Docker = p.existingDocker
	_, err = p.Backend.UpdateSite(*cluster)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

func (p *updatePhaseInit) upsertServiceUser() error {
	cluster, err := p.Backend.GetLocalSite(defaults.SystemAccountID)
	if err != nil {
		return trace.Wrap(err)
	}

	if !cluster.ServiceUser.IsEmpty() {
		// Nothing to do
		return nil
	}

	p.Info("Create service user.")
	user, err := install.GetOrCreateServiceUser(defaults.ServiceUserID, defaults.ServiceGroupID)
	if err != nil {
		return trace.Wrap(err,
			"failed to lookup/create service user %q", defaults.ServiceUser)
	}

	cluster.ServiceUser.Name = user.Name
	cluster.ServiceUser.UID = strconv.Itoa(user.UID)
	cluster.ServiceUser.GID = strconv.Itoa(user.GID)

	_, err = p.Backend.UpdateSite(*cluster)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

func (p *updatePhaseInit) createAdminAgent() error {
	p.Info("Create admin agent user.")
	// create a cluster admin agent as it may not exist yet
	// when upgrading from older versions
	email := storage.ClusterAdminAgent(p.Cluster.Domain)
	_, err := p.Users.CreateClusterAdminAgent(p.Cluster.Domain, storage.NewUser(email, storage.UserSpecV2{
		AccountID:   p.Cluster.AccountID,
		ClusterName: p.Cluster.Domain,
	}))
	if err != nil && !trace.IsAlreadyExists(err) {
		return trace.Wrap(err)
	}
	return nil
}

// NewUpdatePhaseInit creates a new update init phase executor
func NewUpdatePhaseInitServer(
	p fsm.ExecutorParams,
	localPackages pack.PackageService,
	clusterName string,
	logger log.FieldLogger,
) (*updatePhaseInitServer, error) {
	if p.Phase.Data.Update == nil || len(p.Phase.Data.Update.Servers) == 0 {
		return nil, trace.BadParameter("no server specified for phase %q", p.Phase.ID)
	}
	return &updatePhaseInitServer{
		FieldLogger:   logger,
		localPackages: localPackages,
		clusterName:   clusterName,
		server:        p.Phase.Data.Update.Servers[0],
	}, nil
}

// updateExistingPackageLabels updates labels on existing packages
// so the system package pull step can find and pull correct package updates.
//
// For legacy runtime packages ('planet-master' and 'planet-node')
// the sibling runtime package (i.e. 'planet-master' on a regular node
// and vice versa), will be updated to _not_ include the installed label
// to simplify the search
func (p *updatePhaseInitServer) updateExistingPackageLabels() error {
	installedRuntime := p.server.Runtime.Installed
	runtimeConfigLabels, err := updateRuntimeConfigLabels(p.localPackages, installedRuntime)
	if err != nil {
		return trace.Wrap(err)
	}
	teleportConfigLabels, err := updateTeleportConfigLabels(p.localPackages, p.clusterName)
	if err != nil {
		return trace.Wrap(err)
	}
	secretLabels, err := updateRuntimeSecretLabels(p.localPackages)
	if err != nil {
		return trace.Wrap(err)
	}
	updates := append(runtimeConfigLabels, secretLabels...)
	updates = append(updates, teleportConfigLabels...)
	updates = append(updates, pack.LabelUpdate{
		Locator: installedRuntime,
		Add:     utils.CombineLabels(pack.RuntimePackageLabels, pack.InstalledLabels),
	})
	if loc.IsLegacyRuntimePackage(installedRuntime) {
		var runtimePackageToClear loc.Locator
		switch installedRuntime.Name {
		case loc.LegacyPlanetMaster.Name:
			runtimePackageToClear = loc.LegacyPlanetNode.WithLiteralVersion(installedRuntime.Version)
		case loc.LegacyPlanetNode.Name:
			runtimePackageToClear = loc.LegacyPlanetMaster.WithLiteralVersion(installedRuntime.Version)
		}
		updates = append(updates, pack.LabelUpdate{
			Locator: runtimePackageToClear,
			Add:     pack.RuntimePackageLabels,
			Remove:  []string{pack.InstalledLabel},
		})
	}
	for _, update := range updates {
		p.Info(update.String())
		err := p.localPackages.UpdatePackageLabels(update.Locator, update.Add, update.Remove)
		if err != nil && !trace.IsNotFound(err) {
			return trace.Wrap(err)
		}
	}
	return nil
}

// Execute prepares the update on the specified server.
func (p *updatePhaseInitServer) Execute(context.Context) error {
	if err := p.updateExistingPackageLabels(); err != nil {
		return trace.Wrap(err, "failed to update existing package labels")
	}
	return nil
}

// Rollback is a no-op for this phase
func (p *updatePhaseInitServer) Rollback(context.Context) error {
	return nil
}

// PreCheck is a no-op for this phase
func (p *updatePhaseInitServer) PreCheck(context.Context) error {
	return nil
}

// PostCheck is no-op for the init phase
func (p *updatePhaseInitServer) PostCheck(context.Context) error {
	return nil
}

type updatePhaseInitServer struct {
	log.FieldLogger
	server        storage.UpdateServer
	localPackages pack.PackageService
	clusterName   string
}

// removeConfiguredPackages removes packages configured during init phase
func (p *updatePhaseInit) removeConfiguredPackages() error {
	// all packages created during this operation were marked
	// with corresponding operation-id label
	p.Info("Removing configured packages.")
	return pack.ForeachPackageInRepo(p.Packages, p.Operation.SiteDomain,
		func(e pack.PackageEnvelope) error {
			if e.HasLabel(pack.OperationIDLabel, p.Operation.ID) {
				p.Infof("Removing package %q.", e.Locator)
				return p.Packages.DeletePackage(e.Locator)
			}
			return nil
		})
}

// removeInvalidObjectPeers removes object peers that are no longer
// in the cluster
func (p *updatePhaseInit) removeInvalidObjectPeers() error {
	peers, err := p.Backend.GetPeers()
	if err != nil {
		return trace.Wrap(err)
	}
	var errors []error
	for _, peer := range peers {
		if !p.invalidPeers(peer.ID) {
			continue
		}
		p.WithField("peer", peer).Info("Remove invalid peer.")
		if err := p.Backend.DeletePeer(peer.ID); err != nil {
			// Continue with other peers but collect errors to report
			errors = append(errors, err)
		}
	}
	return trace.NewAggregate(errors...)
}

// invalidPeers returns true if all given peers are invalid (no longer in the cluster)
func (p *updatePhaseInit) invalidPeers(peers ...string) (invalid bool) {
	for _, peer := range peers {
		if utils.StringInSlice(p.existingPeers, peer) {
			return false
		}
	}
	return true
}

func updateRuntimeConfigLabels(packages pack.PackageService, installedRuntime loc.Locator) ([]pack.LabelUpdate, error) {
	runtimeConfig, err := pack.FindInstalledConfigPackage(packages, installedRuntime)
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}
	if runtimeConfig != nil {
		// No update necessary
		return nil, nil
	}
	// Fall back to first configuration package
	runtimeConfig, err = pack.FindConfigPackage(packages, installedRuntime)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// Mark this configuration package as installed
	return []pack.LabelUpdate{{
		Locator: *runtimeConfig,
		Add:     pack.InstalledLabels,
	}}, nil
}

func updateTeleportConfigLabels(packages pack.PackageService, clusterName string) ([]pack.LabelUpdate, error) {
	labels := map[string]string{
		pack.PurposeLabel:   pack.PurposeTeleportNodeConfig,
		pack.InstalledLabel: pack.InstalledLabel,
	}
	configEnv, err := pack.FindPackage(packages, func(e pack.PackageEnvelope) bool {
		return e.HasLabels(labels)
	})
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}
	if configEnv != nil {
		// No update necessary
		return nil, nil
	}
	// Fall back to latest available package
	configPackage, err := pack.FindLatestPackageCustom(pack.FindLatestPackageRequest{
		Packages:   packages,
		Repository: clusterName,
		Match: func(e pack.PackageEnvelope) bool {
			return e.Locator.Name == constants.TeleportNodeConfigPackage &&
				(e.HasLabels(pack.TeleportNodeConfigPackageLabels) ||
					e.HasLabels(pack.TeleportLegacyNodeConfigPackageLabels))
		},
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// Mark this configuration package as installed
	return []pack.LabelUpdate{{
		Locator: *configPackage,
		Add:     labels,
	}}, nil
}

func updateRuntimeSecretLabels(packages pack.PackageService) ([]pack.LabelUpdate, error) {
	secretsPackage, err := pack.FindSecretsPackage(packages)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	_, err = pack.FindInstalledPackage(packages, *secretsPackage)
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}
	if err == nil {
		// No update necessary
		return nil, nil
	}
	// Mark this secrets package as installed
	return []pack.LabelUpdate{{
		Locator: *secretsPackage,
		Add:     pack.InstalledLabels,
	}}, nil
}

func removeLegacyUpdateDirectory(log log.FieldLogger) error {
	stateDir, err := state.GetStateDir()
	if err != nil {
		return trace.Wrap(err)
	}
	updateDir := filepath.Join(state.GravityUpdateDir(stateDir), "gravity")
	fi, err := os.Stat(updateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return trace.ConvertSystemError(err)
	}
	if !fi.IsDir() {
		return nil
	}
	log.WithField("dir", updateDir).Debug("Remove legacy update directory.")
	return trace.ConvertSystemError(os.RemoveAll(updateDir))
}

func clusterPeers(servers []storage.UpdateServer) (peers []string) {
	masters, _ := update.SplitServers(servers)
	peers = make([]string, 0, len(masters))
	for _, server := range masters {
		peers = append(peers, server.ObjectPeerID())
	}
	return peers
}
