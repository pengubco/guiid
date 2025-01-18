# Increasing Integer Identifier (I3D) service
The Increasing Integer Identifier (I3D) generates globally unique, increasing 64 bits integer IDs. I3D can run on one node or many nodes. 
Here, each node is a process running on some server. Each node 
generates ID using the [snowflake ID](https://en.wikipedia.org/wiki/Snowflake_ID) algorithm. By default, I3D uses 10 bits to identify the node and thus the service supports 1024 nodes. 
Each node can generate 4,096 ids per millisecond. You can tune the number of bits for nodes. 

If your use case requires the following, then I3D is a good fit.
1. An increasing 64 bits integer ID. 
2. Globally unique. 
3. Highly available. 
4. Highly scalable.  
5. Low latency. P99 lower than 1ms.

I3D chooses consistency over availability. High availability is achieved by running on a cluster of nodes coordinated by etcd service. 
As long as the etcd service is available, then the I3D is available. This is because each I3D node is stateless.
See [design](./design.md) for details.

## How to run the code?
Nodes are stateless and do not depend on persisted storage. Nodes do not talk with each other either. Instead, nodes coordinate
through [etcd](https://etcd.io/).
Each node requires read and write permission of a prefix on etcd. In order to run locally, follow the [Appendix 1](#1-Prepare-a-local-etcd-server) 
to setup a etcd server on the localhost. Try it out in terminal with with a gRPC client like [Insomnia](https://insomnia.rest/) or [grpc-client-cli](https://github.com/vadimi/grpc-client-cli).

### Run one I3D node as a Go program
```bash
# Step 1. Build
make build-go
# Step 2. Start the etcd service
etcd 
# Step 3. Follow Appendix-1 to setup prefixes and permission on etcd.
# Step 4. Use the example config file. 
./build/i3d -config_from_file=./example/config/localhost.json
# Step 5. Have fun.
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID localhost:7669
echo '2' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextMultipleIDs localhost:7669
```

### Run one I3D server as a Docker container
The latest build image is published on DockerHub [pengubco/i3d](https://hub.docker.com/repository/docker/pengubco/i3d/general). 
```bash
make build-docker
# assume the config file is ./example/config/docker.json
docker run --rm --name i3d --mount type=bind,source="$(pwd)"/example/config,target=/app-config -p 7669:7669 i3d -config_from_file=/app-config/docker.json
# make gRPC call to get ID
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID localhost:7669
echo '2' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextMultipleIDs localhost:7669
```

### Run a cluster of five I3D nodes in Kubernetes.
The setup is for demo purpose. 
```bash
# Step 1. create a namespace to demo
kubectl create namespace i3d
# Step 2. create a pod running etcd and expose it through NodePort Service.
kubectl create -f ./example/pod-etcd.yaml 
# Step 3. setup user, role in etcd. Execute commands in Appendix 1 inside the etcd pod. 
# Step 4. create a configmap to store the json config
kubectl create -f ./example/configmap.yaml 
# Step 5. deploy the i3d service
kubectl create -f ./example/deployment.yaml 

# make gRPC call to get snowflake ID. 
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID [hostname-or-ip-of-node]:30001
echo '2' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextMultipleIDs [hostname-or-ip-of-node]:30001
```

## Contribute
Start with opening an issue. 

## Appendix
### 1. Prepare a local etcd server. 
Step 1. [Install](https://etcd.io/docs/v3.5/install/) the etcd 3.5 and start it. 
Step 2. Setup user, role and permission. Learn more about etcd [RBAC](https://etcd.io/docs/v3.5/op-guide/authentication/rbac/).
```bash
etcdctl --user root:root user add i3d --new-user-password="passwd"
etcdctl --user root:root role add i3d
etcdctl --user root:root role grant-permission --prefix=true i3d readwrite /i3d/ 
etcdctl --user root:root user grant i3d i3d
# verify read and write
etcdctl --user i3d:passwd put /i3d/k1 v1
etcdctl --user i3d:passwd get /i3d/k1 
etcdctl --user i3d:passwd del /i3d/k1 
```
