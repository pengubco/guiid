# GUIID service
The Globally Unique Integer Identifier (GUIID) Service generates globally unique, increasing 64 bits integer IDs. GUUID runs on a a cluster of servers. Each server 
generates ID using the [snowflake ID](https://en.wikipedia.org/wiki/Snowflake_ID) algorithm. By default, the service uses 10 bits to identify the server and thus the service supports 1024 servers. 
Each server can generate 4,096 ids per millisecond. 

If your use case requires the following, GUIID service is a good fit.
1. An increasing 64 bits integer ID. 
2. Globally unique. No collision of IDs. 
3. Highly available. The ID is on the critical path and must be fault tolerant of single node failure. 
4. Highly scalable.  
5. Low latency. P99 lower than 1ms.

Does GUIID defy the [CAP theorem](https://en.wikipedia.org/wiki/CAP_theorem)? 

No. GUIID service is a distributed system so it can guarantee only two out of three properties of CAP.
GUIID chooses consistency and partition tolerance. High availability is achieved by running on a cluster of servers coordinated by etcd cluster. 
As long as more than half of the etcd nodes are healthy and one GUIID server is healthy, the GUIID service is available.  

See [design](./design.md) for how we coordinate a collection of stateless servers to generate globally unique and increasing IDs.

## How to run the code?
Servers are stateless and do not depend on persisted storage. Servers do not talk with each other. Instead, servers coordinate
through [etcd](https://etcd.io/).
Each server needs read and write permission of a prefix on etcd. In order to run locally, follow the [Appendix 1](#1-Prepare-a-local-etcd-server) 
to setup a etcd server on the localhost. Also install a gRPC client that can get snowflake ID from the GUIID service. 
[Insomnia](https://insomnia.rest/) or [grpc-client-cli](https://github.com/vadimi/grpc-client-cli) would work.

### Run one GUIID server as a Go program
```bash
make build-go
# assume the config file is ./example/config/localhost.json
./build/guiid -config_from_file=./example/config/localhost.json
# make gRPC call to get a new ID
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID localhost:7669
```

### Run one GUIID server as a Docker container
The latest build image is published on DockerHub [pengubco/guiid](https://hub.docker.com/repository/docker/pengubco/guiid/general). 
```bash
make build-docker
# assume the config file is ./example/config/docker.json
docker run --rm --name guiid --mount type=bind,source="$(pwd)"/example/config,target=/app-config -p 7669:7669 guiid -config_from_file=/app-config/docker.json
# make gRPC call to get ID
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID localhost:7669
```

### Run a cluster of 5 GUIID servers in Kubernetes.
The setup is for demo purpose. 
```bash
# Step 1. create a namespace to demo
kubectl create namespace guiid
# Step 2. create a pod running etcd and expose it through NodePort Service.
kubectl create -f ./example/pod-etcd.yaml 
# Step 3. setup user, role in etcd. Execute commands in Appendix 1 inside the etcd pod. 
# Step 4. create a configmap to store the json config
kubectl create -f ./example/configmap.yaml 
# Step 5. deploy the guiid service
kubectl create -f ./example/deployment.yaml 

# make gRPC call to get snowflake ID. 
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID [hostname-or-ip-of-node]:30001
```

## Contribute
Thank you for considering contributing to GUIID! We welcome contributions of all kinds, whether you're an experienced developer or just starting out. 
Feel free to ask questions, report issues and create pull requests.

## Appendix
### 1. Prepare a local etcd server. 
Step 1. [Install](https://etcd.io/docs/v3.5/install/) the etcd 3.5 and start it. 
Step 2. Setup user, role and permission. Learn more about etcd [RBAC](https://etcd.io/docs/v3.5/op-guide/authentication/rbac/).
```bash
etcdctl --user root:root user add guiid --new-user-password="passwd"
etcdctl --user root:root role add guiid
etcdctl --user root:root role grant-permission --prefix=true guiid readwrite /guiid/ 
etcdctl --user root:root user grant guiid guiid
# verify read and write
etcdctl --user guiid:passwd put /guiid/k1 v1
etcdctl --user guiid:passwd get /guiid/k1 
etcdctl --user guiid:passwd del /guiid/k1 
```
