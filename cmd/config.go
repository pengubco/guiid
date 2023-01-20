package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the configuration of the application.
type Config struct {
	EtcdEndpoints []string `json:"etcd_endpoints"`
	EtcdPrefix    string   `json:"etcd_prefix"`
	EtcdUsername  string   `json:"etcd_username"`
	EtcdPassword  string   `json:"etcd_password"`

	// Maximum number of servers the snowflake ID cluster can support.
	MaxServerCnt int `json:"max_server_cnt"`
}

func loadConfigurationFromFile(f string) (*Config, error) {
	var c Config
	var b []byte
	var err error
	if b, err = os.ReadFile(f); err != nil {
		return nil, fmt.Errorf("cannot read file %s. %v", f, err)
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("cannot read config from JSON, %v", err)
	}
	return &c, nil
}
