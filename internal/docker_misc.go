package internal

import (
	"github.com/docker/docker/api/types/container"
)

type HostCfgBuilder struct {
	cfg *container.HostConfig
}

func NewHostCfgBuilder() *HostCfgBuilder {
	return &HostCfgBuilder{
		cfg: &container.HostConfig{},
	}
}

func (b *HostCfgBuilder) AttachBindsToHostConfig(binds []string) *HostCfgBuilder {
	b.cfg.Binds = binds
	return b
}

func (b *HostCfgBuilder) AttachPrivilegedToHostConfig(privileged bool) *HostCfgBuilder {
	b.cfg.Privileged = privileged
	return b
}

func (b *HostCfgBuilder) AttachResourcesToHostConfig(resources container.Resources) *HostCfgBuilder {
	b.cfg.Resources = resources
	return b
}

func (b *HostCfgBuilder) AttachCapAddToHostConfig(caps []string) *HostCfgBuilder {
	b.cfg.CapAdd = caps
	return b
}

func (b *HostCfgBuilder) AttachPidModeToHostConfig(pidMode container.PidMode) *HostCfgBuilder {
	b.cfg.PidMode = pidMode
	return b
}

func (b *HostCfgBuilder) AttachNetworkModeToHostConfig(networkMode container.NetworkMode) *HostCfgBuilder {
	b.cfg.NetworkMode = networkMode
	return b
}

func (b *HostCfgBuilder) AttachIPCModeToHostConfig(ipcMode container.IpcMode) *HostCfgBuilder {
	b.cfg.IpcMode = ipcMode
	return b
}

func (b *HostCfgBuilder) AttachLogConfigToHostConfig(logConfig container.LogConfig) *HostCfgBuilder {
	b.cfg.LogConfig = logConfig
	return b
}

func (b *HostCfgBuilder) Build() *container.HostConfig {
	return b.cfg
}
