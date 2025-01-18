# Coordinate a cluster of nodes running snowflake ID algorithm

The snowflake ID algorithm generates a 64 bits integer from the following threepieces of information. 
1. timestamp in millisecond. The timestamp is encoded in 41 bits. 
2. identifier of the process/node running the algorithm. The node ID is encoded in 10 bits.
3. sequence number for the millisecond. The sequence number is encoded in 12 bits.

When we run snowflake ID on a single node, the only thing we need to worry about is the clock drift where the timestamp goes backward. However, when we run multiple nodes. We need to consider more:
1. No two nodes should use the same node identifier at the same time. Otherwise, two nodes will generate duplicate IDs.
2. When a node fails, its node identifier must be reused by a new healthy node. Otherwise, we will run out of available node identifiers. 
3. When a node identifier is reused, there must be no clock drift between the failed node and the new node. That is, if the timestamp `t0` has been used by the failed node `node-1`, any timestamp `t1` used by the new node 
`node-2` must be larger than `t0`. 

## etcd
We use etcd to coordinate nodes. [Etcd](https://etcd.io/) is a distributed, reliable key-value store for the most critical data of a distributed system. 
For each node identifier, there are two KVs in etcd. Assuming the node id is 10.
1. key: "workers/live/10". value: timestamp when the node id, 10, was assigned. We call this KV liveness key. 
2. key: "workers/last_reported_time/10". value: timestamp the node last reported the timestamp it used. We call this KV freshness key.

What etcd features we use?
1. [etcd lease](https://etcd.io/docs/v3.5/learning/api/#lease-api) is associated with a key. When the lease expires, the associated key is deleted. When a node starts, it starts a lease on the liveness key and it renews its lease while it is healthy. 
When the node fails, the lease expires and the liveness key is deleted. 
1. [etcd transaction](https://etcd.io/docs/v3.5/learning/api/#transaction) is an atomic If/Then/Else construct over the key-value store. Transactions are also serializable. Using transactions to query and set KVs on etcd gives us concurrency without using [lock](https://etcd.io/docs/v3.5/dev-guide/api_concurrency_reference_v3/). 
The problem of using lock properly is telling the owner of the lock. Within a single process, it is not difficult to tell the owner. However, in distributed systems, telling owner of a lock is difficult. 

## walkthrough of node joining and leaving the cluster
Step 1. A node wants to join the cluster. The node iterates node identifiers to find a free node identifier. A node identifier is free if the liveness key does not exist on Etcd. If there is no free node identifier, the node exits. No free node identifier implies the cluster is at its capacity. Otherwise, goes to Step 2. 

Step 2. The node finds a free node identifier, say, 10. The node checks whether its clock is newer than the freshness key, "workers/last_reported_time/10". If the node's clock is behind the timestamp recorded in the freshness KV too much, the node exits. Otherwise, goes to Step 3.

Step 3. The node started a lease and associate it with the key "workers/live/10". The node refresh the lease periodically till it exits. 

Step 4. The node 10 serves requests. In the meantime, the node periodically updates the freshness KV with its latest timestamp. The update is done in a transaction which has condition that lease the node owns is the same lease associated with the liveness key. 
The reason we check lease is that the node can lose network long enough that another node has claimed the node identifier 10. If the lease check fails, the node exits. Otherwise, repeat Step 4 to serve new requests. 

Step 5. The node exits. After timeout window passes, the lease of the liveness KV expires and "workers/last_reported_time/10" is removed. This indicates the node leaves the cluster. Another node can join the cluster starting with the step 1. 
 