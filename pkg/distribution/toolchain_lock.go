package distribution

import (
	"encoding/json"
	"fmt"
	"io"
)

const ToolchainLockVersion = 1

type ToolchainLock struct {
	SchemaVersion int                `json:"schema_version"`
	Kind          string             `json:"kind"`
	Toolchains    []ReleaseComponent `json:"toolchains"`
}

func ReadToolchainLock(reader io.Reader) (ToolchainLock, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var lock ToolchainLock
	if err := decoder.Decode(&lock); err != nil {
		return ToolchainLock{}, fmt.Errorf("decode toolchain lock: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ToolchainLock{}, fmt.Errorf("decode toolchain lock: trailing JSON data")
	}
	if lock.SchemaVersion != ToolchainLockVersion {
		return ToolchainLock{}, fmt.Errorf("unsupported toolchain lock schema %d", lock.SchemaVersion)
	}
	if lock.Kind != "peeper-toolchains" {
		return ToolchainLock{}, fmt.Errorf("unsupported toolchain lock kind %q", lock.Kind)
	}
	byID := make(map[string]bool, len(lock.Toolchains))
	byHost := make(map[releaseHost]bool, len(lock.Toolchains))
	for _, component := range lock.Toolchains {
		if err := validateReleaseComponent(component); err != nil {
			return ToolchainLock{}, fmt.Errorf("toolchain lock: %w", err)
		}
		if component.Kind != PackKindToolchain {
			return ToolchainLock{}, fmt.Errorf("toolchain lock component %q has kind %q", component.ID, component.Kind)
		}
		if byID[component.ID] {
			return ToolchainLock{}, fmt.Errorf("toolchain lock repeats component %q", component.ID)
		}
		byID[component.ID] = true
		host := releaseHost{os: component.OS, arch: component.Arch}
		if byHost[host] {
			return ToolchainLock{}, fmt.Errorf("toolchain lock repeats target %s/%s", host.os, host.arch)
		}
		byHost[host] = true
	}
	for _, host := range supportedReleaseHosts {
		if !byHost[host] {
			return ToolchainLock{}, fmt.Errorf("toolchain lock lacks target %s/%s", host.os, host.arch)
		}
	}
	if len(byHost) != len(supportedReleaseHosts) {
		return ToolchainLock{}, fmt.Errorf("toolchain lock contains unsupported target")
	}
	return lock, nil
}

func (lock ToolchainLock) Component(targetOS, targetArch string) (ReleaseComponent, error) {
	for _, component := range lock.Toolchains {
		if component.OS == targetOS && component.Arch == targetArch {
			return component, nil
		}
	}
	return ReleaseComponent{}, fmt.Errorf("toolchain lock lacks target %s/%s", targetOS, targetArch)
}
