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

package mage

import (
	"context"
	"path/filepath"

	"github.com/gravitational/trace"
	"github.com/magefile/mage/mg"
)

type Cluster mg.Namespace

// Gravity builds the reference gravity cluster image.
func (Cluster) Gravity() (err error) {
	mg.Deps(Build.Go, Package.Telekube)

	m := root.Target("cluster:gravity")
	defer func() { m.Complete(err) }()

	_, err = m.Exec().SetEnv("GRAVITY_K8S_VERSION", k8sVersion).Run(context.TODO(),
		filepath.Join(consistentBinDir(), "tele"),
		"--debug",
		"build",
		"assets/telekube/resources/app.yaml",
		"-f",
		"--version", buildVersion,
		"--state-dir", consistentStateDir(),
		"--skip-version-check",
		"-o", filepath.Join(consistentBuildDir(), "gravity.tar"),
	)
	return trace.Wrap(err)
}

// Wormhole builds the reference gravity cluster image based on wormhole networking.
func (Cluster) Wormhole() (err error) {
	mg.Deps(Build.Go, Package.Telekube)

	m := root.Target("cluster:wormhole")
	defer func() { m.Complete(err) }()

	_, err = m.Exec().SetEnv("GRAVITY_K8S_VERSION", k8sVersion).Run(context.TODO(),
		filepath.Join(consistentBinDir(), "tele"),
		"--debug",
		"build",
		"assets/wormhole/resources/app.yaml",
		"-f",
		"--version", buildVersion,
		"--state-dir", consistentStateDir(),
		"--skip-version-check",
		"-o", filepath.Join(consistentBuildDir(), "wormhole.tar"),
	)
	return trace.Wrap(err)
}