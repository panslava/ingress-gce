#!/bin/bash

project="s3ns-system:gke-tpc-prober"

gcloud-tpc() {
    /google/bin/users/chaodai/cloud/storage/tpc/tools/gcloud_tpc\:gcloud_tpc_sig/main "$@"
}

# Setup the environment for running the e2e tests from your
# desktop.

set -e
parseCluster() {
    # These are all globals.
    net=$1
    subnet=$2
    zone=$3
    selfLink=$4
    net=$(echo ${net} | sed 's+.*networks/\([-a-z0-9]*\).*$+\1+')
    subnet=$(echo ${subnet} | sed 's+.*subnetworks/\([-a-z0-9]*\)$+\1+')
}

parseInstance() {
    local name=$1
    zone="u-france-east1-a"
    # Globals.
    nodeTag=$(gcloud-tpc --universe=thp000 --service-account=test-projects-editor@gke-sa  compute instances describe ${name} --zone ${zone} --format='value(tags.items[0])' --project ${project})
}

clusterName="$1"
clusterLocation="$2"

if [ -z "${clusterName}" ]; then
    echo "Usage: $0 CLUSTER_NAME [LOCATION]"
    echo
    echo "LOCATION is optional if there is only one cluster with CLUSTER_NAME"
    exit 1
fi

fmt='value(networkConfig.network,networkConfig.subnetwork,zone,selfLink,name)'
if [ -z "$clusterLocation" ]; then
    clusters=$(gcloud-tpc --universe=thp000 --service-account=test-projects-editor@gke-sa container clusters list --format="${fmt}" --filter="name=${clusterName}" --project ${project})
else
    clusters=$(gcloud-tpc --universe=thp000 --service-account=test-projects-editor@gke-sa container clusters list --format="${fmt}" --filter="name=${clusterName} location=${clusterLocation}" --project ${project})
fi
if [ $(echo "${cluster}" | wc -l) -gt 1 ]; then
    echo "ERROR: more than one cluster matches '${clusterName}'"
fi
parseCluster ${clusters}
if [ -z  "${clusters}" ]; then
    echo "ERROR: No cluster '${clusterName}' found"
    exit 1
fi

# VM instances names created by gke are truncated up to 24 characters
nodesPrefix=$(echo "gke-$clusterName" | head -c 24)
# Get one instance from cluster's instance group
echo "Found cluster in zone $zone"
#instanceGroupUrls=$(gcloud-tpc --universe=thp000 --service-account=test-projects-editor@gke-sa  container  clusters describe $clusterName --zone $zone --format='value(instanceGroupUrls)'| awk -F"/" '{print $NF}')
echo "Instance group name: $instanceGroupUrls"
#instance=$(gcloud-tpc --universe=thp000 --service-account=test-projects-editor@gke-sa   container  clusters describe $clusterName --zone $zone --format='value(instanceGroupUrls)' | awk -F"/" '{print $NF}' | xargs -I {} gcloud-tpc --universe=thp000 --service-account=test-projects-editor@gke-sa --project  compute instance-groups list-instances {} --zone $zone | grep  RUNNING | awk '{print $1}' | tail -n 1)
instance="gke-ds-hdb-7-nap-1c6do1p4-9685e223-8zxq"

if [ -z "${instance}" ]; then
  echo "ERROR: No running instances for cluster"
  exit 1;
fi

parseInstance ${instance}

if [ -z  "${instance}" ]; then
    echo "ERROR: No nodes matching '${clusterName}' found"
    exit 1
fi

gceConf="/tmp/gce.conf"
echo "Writing ${gceConf}"
echo "----"
cat <<EOF |  tee ${gceConf}
[global]
token-url = nil
project-id = ${project}
network-name = ${net}
subnetwork-name = ${subnet}
node-instance-prefix = ${nodesPrefix}
node-tags = ${nodeTag}
local-zone = ${zone}
EOF

echo "Run glbc with hack/run-glbc.sh"