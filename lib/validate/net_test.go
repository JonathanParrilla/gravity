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

package validate

import (
	"testing"

	"gopkg.in/check.v1"
)

func Test(t *testing.T) { check.TestingT(t) }

type S struct{}

var _ = check.Suite(&S{})

func (*S) TestValidateKubernetesSubnets(c *check.C) {
	type testCase struct {
		podCIDR     string
		serviceCIDR string
		ok          bool
		description string
	}
	testCases := []testCase{
		{
			podCIDR:     "10.244.0.0/16",
			serviceCIDR: "10.100.0.0/16",
			ok:          true,
			description: "default subnets should validate",
		},
		{
			podCIDR:     "10.244.0.0-10.244.255.0",
			ok:          false,
			description: "pod subnet is not a valid CIDR",
		},
		{
			serviceCIDR: "10.100.0.0-10.100.255.0",
			ok:          false,
			description: "service subnet is not a valid CIDR",
		},
		{
			podCIDR:     "10.200.0.0/20",
			ok:          false,
			description: "pod subnet is too small",
		},
		{
			podCIDR:     "10.100.0.0/16",
			serviceCIDR: "10.100.100.0/16",
			ok:          false,
			description: "pod and service subnets overlap",
		},
	}
	for _, tc := range testCases {
		err := KubernetesSubnets(tc.podCIDR, tc.serviceCIDR)
		if tc.ok {
			c.Assert(err, check.IsNil, check.Commentf(tc.description))
		} else {
			c.Assert(err, check.NotNil, check.Commentf(tc.description))
		}
	}
}
