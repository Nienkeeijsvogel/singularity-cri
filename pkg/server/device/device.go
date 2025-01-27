// Copyright (c) 2018-2019 Sylabs, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Portions of this file were derived from github.com/nvidia/k8s-device-plugin
// under the following license:
//
// Copyright (c) 2017, NVIDIA CORPORATION. All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions
// are met:
// * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
// * Redistributions in binary form must reproduce the above copyright
// notice, this list of conditions and the following disclaimer in the
// documentation and/or other materials provided with the distribution.
// * Neither the name of NVIDIA CORPORATION nor the names of its
// contributors may be used to endorse or promote products derived
// from this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS ``AS IS'' AND ANY
// EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
// IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
// PURPOSE ARE DISCLAIMED.  IN NO EVENT SHALL THE COPYRIGHT OWNER OR
// CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
// EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
// PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
// PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY
// OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package device

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	"github.com/golang/glog"
	"github.com/sylabs/singularity-cri/pkg/singularity"
	"github.com/sylabs/singularity-cri/pkg/singularity/runtime"
	"github.com/sylabs/singularity/pkg/util/gpu"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	k8sDP "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1beta1"
)

var (
	// ErrNoGPUs is returned when device plugin is unable to
	// detect any GPU device on the host.
	ErrNoGPUs = fmt.Errorf("GPUs are not found on this host")

	// ErrUnableToLoad is returned when device plugin is unable to
	// detect loaded graphic driver on the host or unable to load
	// NVML shared library.
	ErrUnableToLoad = fmt.Errorf("unable to load: check libnvidia-ml.so.1 library and graphic drivers")
)

// SingularityDevicePlugin is Singularity implementation of a DevicePluginServer
// interface that allows containers to request nvidia GPUs.
type SingularityDevicePlugin struct {
	devices  map[string]*nvml.Device
	hospital map[string]string
	confDir  string

	done         chan struct{}
	unhealthyDev <-chan string
}

// NewSingularityDevicePlugin initializes and returns Singularity device plugin
// that allows us to access nvidia GPUs on host. It fails if there is no
// graphic driver installed on host or if Nvidia Management Library (NVML)
// fails to load.
func NewSingularityDevicePlugin() (*SingularityDevicePlugin, error) {
	_, err := exec.LookPath(singularity.RuntimeName)
	if err != nil {
		return nil, fmt.Errorf("could not find %s on this machine: %v", singularity.RuntimeName, err)
	}
	config, err := runtime.NewCLIClient().BuildConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get build config: %v", err)
	}

	glog.V(1).Infof("Loading NVML")
	if err = nvml.Init(); err != nil {
		glog.Errorf("Could not initialize NVML library: %v", err)
		return nil, ErrUnableToLoad
	}

	dp := &SingularityDevicePlugin{
		done:    make(chan struct{}),
		confDir: config.SingularityConfdir,
	}
	defer func() {
		if err != nil {
			glog.Errorf("Shutting down device plugin due to %v", err)
			dp.Shutdown()
		}
	}()

	v, err := nvml.GetDriverVersion()
	if err != nil {
		glog.Errorf("Could not get driver version: %v", err)
		return nil, ErrUnableToLoad
	}
	glog.V(1).Infof("Found graphic driver of version %v", v)

	devices, err := getDevices()
	if err != nil {
		return nil, fmt.Errorf("could not get available devices: %v", err)
	}
	if len(devices) == 0 {
		return nil, ErrNoGPUs
	}

	dp.devices = make(map[string]*nvml.Device, len(devices))
	dp.hospital = make(map[string]string, len(devices))
	devIDs := make([]string, len(devices))
	for i, dev := range devices {
		dp.devices[dev.UUID] = dev
		dp.hospital[dev.UUID] = k8sDP.Healthy
		devIDs[i] = dev.UUID
	}

	dp.unhealthyDev, err = monitorGPUs(dp.done, devIDs)
	if err != nil {
		return nil, fmt.Errorf("could not start GPU monitoring: %v", err)
	}

	return dp, nil
}

// Shutdown shuts down device plugin and any GPU monitoring activity.
func (dp *SingularityDevicePlugin) Shutdown() error {
	glog.V(3).Infof("Cancelling GPU monitoring")
	close(dp.done)
	return nvml.Shutdown()
}

// GetDevicePluginOptions returns options to be communicated with Device Manager.
func (*SingularityDevicePlugin) GetDevicePluginOptions(context.Context, *k8sDP.Empty) (*k8sDP.DevicePluginOptions, error) {
	return &k8sDP.DevicePluginOptions{}, nil
}

// ListAndWatch returns a stream of List of Devices. Whenever a Device state changes
// or a Device disappears, ListAndWatch returns the new list.
func (dp *SingularityDevicePlugin) ListAndWatch(_ *k8sDP.Empty, srv k8sDP.DevicePlugin_ListAndWatchServer) error {
	devList := dp.listK8sDevices()
	glog.V(3).Infof("Sending initial device list: %v", devList)
	err := srv.Send(&k8sDP.ListAndWatchResponse{Devices: devList})
	if err != nil {
		return status.Errorf(codes.Unknown, "could not send initial devices state: %v", err)
	}
	for {
		select {
		case <-dp.done:
			return nil
		case devID := <-dp.unhealthyDev:
			dp.hospital[devID] = k8sDP.Unhealthy
			glog.Warningf("Device %s is in hospital", devID)

			err := srv.Send(&k8sDP.ListAndWatchResponse{Devices: dp.listK8sDevices()})
			if err != nil {
				return status.Errorf(codes.Unknown, "could not send updated devices state: %v", err)
			}
		}
	}
}

// Allocate is called during container creation so that the Device Plugin can run
// device specific operations and instruct Kubelet of the steps to make the Device
// available in the container.
func (dp *SingularityDevicePlugin) Allocate(ctx context.Context, req *k8sDP.AllocateRequest) (*k8sDP.AllocateResponse, error) {
	nvLibs, nvBins, err := nvidia.Paths(dp.confDir, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not search NVIDIA files: %v", err)
	}
	glog.V(4).Infof("NVIDIA paths are %v and %v", nvLibs, nvBins)

	nvDevs, err := nvidia.Devices(false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not search NVIDIA complementary devices: %v", err)
	}
	glog.V(4).Infof("NVIDIA complementary devices are %v", nvDevs)

	nvidiaMounts := make([]*k8sDP.Mount, 0, len(nvLibs)+len(nvBins))
	for _, libPath := range nvLibs {
		nvidiaMounts = append(nvidiaMounts, &k8sDP.Mount{
			ContainerPath: libPath,
			HostPath:      libPath,
			ReadOnly:      true,
		})
	}
	for _, binPath := range nvBins {
		nvidiaMounts = append(nvidiaMounts, &k8sDP.Mount{
			ContainerPath: binPath,
			HostPath:      binPath,
			ReadOnly:      true,
		})
	}

	allocateResponses := make([]*k8sDP.ContainerAllocateResponse, 0, len(req.ContainerRequests))
	for _, allocateRequest := range req.ContainerRequests {
		nvidiaDevices := make([]*k8sDP.DeviceSpec, 0, len(nvDevs)+len(allocateRequest.DevicesIDs))
		for _, nvDev := range nvDevs {
			nvidiaDevices = append(nvidiaDevices, &k8sDP.DeviceSpec{
				ContainerPath: nvDev,
				HostPath:      nvDev,
				Permissions:   "rw",
			})
		}
		for _, devID := range allocateRequest.DevicesIDs {
			device := dp.devices[devID]
			nvidiaDevices = append(nvidiaDevices, &k8sDP.DeviceSpec{
				ContainerPath: device.Path,
				HostPath:      device.Path,
				Permissions:   "rw",
			})
		}
		allocateResponses = append(allocateResponses, &k8sDP.ContainerAllocateResponse{
			Mounts:  nvidiaMounts,
			Devices: nvidiaDevices,
		})
	}
	return &k8sDP.AllocateResponse{
		ContainerResponses: allocateResponses,
	}, nil
}

// PreStartContainer is called, if indicated by Device Plugin during registration phase,
// before each container start. Device plugin can run device specific operations
// such as resetting the device before making devices available to the container.
func (*SingularityDevicePlugin) PreStartContainer(context.Context, *k8sDP.PreStartContainerRequest) (*k8sDP.PreStartContainerResponse, error) {
	return &k8sDP.PreStartContainerResponse{}, nil
}

func (dp *SingularityDevicePlugin) listK8sDevices() []*k8sDP.Device {
	devices := make([]*k8sDP.Device, 0, len(dp.hospital))
	for devID, health := range dp.hospital {
		devices = append(devices, &k8sDP.Device{
			ID:     devID,
			Health: health,
		})
	}
	return devices
}
