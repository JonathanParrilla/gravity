#!/bin/bash
set -eu -o pipefail

readonly UPGRADE_FROM_DIR=${1:-$(pwd)/../upgrade_from}

# UPGRADE_MAP maps gravity version -> list of OS releases to upgrade from
declare -A UPGRADE_MAP

# latest patch release on this branch, keep this up to date
UPGRADE_MAP[6.1.25]="ubuntu:16"

# latest patch release on previous compatible LTS, keep this up to date
UPGRADE_MAP[5.5.41]="ubuntu:16"

# first release from this release & last LTS, these don't need to change until a new major/minor release
UPGRADE_MAP[6.1.0]="ubuntu:16"
UPGRADE_MAP[5.5.0]="ubuntu:16"

# important versions in the field, per:
# https://github.com/gravitational/robotest/issues/155#issuecomment-589743687
UPGRADE_MAP[5.5.28]="ubuntu:16"
UPGRADE_MAP[5.5.36]="ubuntu:16"
UPGRADE_MAP[5.5.38]="ubuntu:16"
UPGRADE_MAP[5.5.40]="ubuntu:16"

readonly GET_GRAVITATIONAL_IO_APIKEY=${GET_GRAVITATIONAL_IO_APIKEY:?API key for distribution Ops Center required}
readonly GRAVITY_BUILDDIR=${GRAVITY_BUILDDIR:?Set GRAVITY_BUILDDIR to the build directory}
readonly ROBOTEST_SCRIPT=$(mktemp -d)/runsuite.sh

# number of environment variables are expected to be set
# see https://github.com/gravitational/robotest/blob/master/suite/README.md
export ROBOTEST_VERSION=${ROBOTEST_VERSION:-2.0.0}
export ROBOTEST_REPO=quay.io/gravitational/robotest-suite:$ROBOTEST_VERSION
export WAIT_FOR_INSTALLER=true
export INSTALLER_URL=$GRAVITY_BUILDDIR/telekube.tar
export GRAVITY_URL=$GRAVITY_BUILDDIR/gravity
export TAG=$(git rev-parse --short HEAD)
# cloud provider that test clusters will be provisioned on
# see https://github.com/gravitational/robotest/blob/master/infra/gravity/config.go#L72
export DEPLOY_TO=${DEPLOY_TO:-gce}
export GCL_PROJECT_ID=${GCL_PROJECT_ID:-"kubeadm-167321"}
export GCE_REGION="northamerica-northeast1,us-west1,us-east1,us-east4,us-central1"
# GCE_VM tuned down from the Robotest's 7 cpu default in 09cec0e49e9d51c3603950209cec3c26dfe0e66b
# We should consider changing Robotest's default so that we can drop the override here. -- 2019-04 walt
export GCE_VM=${GCE_VM:-custom-4-8192}
# Parallelism & retry, tuned for GCE
export PARALLEL_TESTS=${PARALLEL_TESTS:-4}
export REPEAT_TESTS=${REPEAT_TESTS:-1}

function build_resize_suite {
  cat <<EOF
 resize={"to":3,"flavor":"one","nodes":1,"role":"node","state_dir":"/var/lib/telekube","os":"ubuntu:16","storage_driver":"overlay2"}
 shrink={"nodes":3,"flavor":"three","role":"node","os":"redhat:7"}
EOF
}

function build_upgrade_step {
  local usage="$FUNCNAME os release storage-driver cluster-size"
  local os=${1:?$usage}
  local release=${2:?$usage}
  local storage_driver=${3:?$usage}
  local cluster_size=${4:?$usage}
  local service_opts='"service_uid":997,"service_gid":994' # see issue #1279
  local suite=''
  suite+=$(cat <<EOF
 upgrade={${cluster_size},${service_opts},"os":"${os}","storage_driver":"${storage_driver}","from":"/telekube_${release}.tar"}
EOF
)
  echo $suite
}

function build_upgrade_suite {
  local suite=''
  local cluster_size='"flavor":"three","nodes":3,"role":"node"'
  for release in ${!UPGRADE_MAP[@]}; do
    for os in ${UPGRADE_MAP[$release]}; do
      suite+=$(build_upgrade_step $os $release 'overlay2' $cluster_size)
      suite+=' '
    done
  done
  echo $suite
}

function build_ops_install_suite {
  local suite=$(cat <<EOF
 install={"installer_url":"/installer/opscenter.tar","nodes":1,"flavor":"standalone","role":"node","os":"ubuntu:16","ops_advertise_addr":"example.com:443"}
EOF
)
  echo $suite
}

function build_install_suite {
  local suite=''
  local test_os="redhat:7"
  local cluster_size='"flavor":"three","nodes":3,"role":"node"'
  suite+=$(cat <<EOF
 install={${cluster_size},"os":"${test_os}","storage_driver":"overlay2"}
EOF
)
  suite+=' '
  suite+=$(build_ops_install_suite)
  echo $suite
}

function build_volume_mounts {
  for release in ${!UPGRADE_MAP[@]}; do
    echo "-v $UPGRADE_FROM_DIR/telekube_${release}.tar:/telekube_${release}.tar"
  done
}

export EXTRA_VOLUME_MOUNTS=$(build_volume_mounts)

suite=$(build_resize_suite)
suite="$suite $(build_upgrade_suite)"
suite="$suite $(build_install_suite)"

echo $suite

mkdir -p $UPGRADE_FROM_DIR
tele login --ops=https://get.gravitational.io:443 --key="$GET_GRAVITATIONAL_IO_APIKEY"
for release in ${!UPGRADE_MAP[@]}; do
  tele pull telekube:$release --output=$UPGRADE_FROM_DIR/telekube_$release.tar
done

docker pull $ROBOTEST_REPO
docker run $ROBOTEST_REPO cat /usr/bin/run_suite.sh > $ROBOTEST_SCRIPT
chmod +x $ROBOTEST_SCRIPT
$ROBOTEST_SCRIPT "$suite"
