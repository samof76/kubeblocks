/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package component

import (
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/leaanthony/debme"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/internal/constant"
	intctrlutil "github.com/apecloud/kubeblocks/internal/controllerutil"
	viper "github.com/apecloud/kubeblocks/internal/viperx"
)

const (
	// http://localhost:<port>/v1.0/bindings/<binding_type>
	checkRoleURIFormat        = "/v1.0/bindings/%s?operation=checkRole&workloadType=%s"
	checkRunningURIFormat     = "/v1.0/bindings/%s?operation=checkRunning"
	checkStatusURIFormat      = "/v1.0/bindings/%s?operation=checkStatus"
	volumeProtectionURIFormat = "/v1.0/bindings/%s?operation=volumeProtection"
)

var (
	//go:embed cue/*
	cueTemplates embed.FS

	// default probe setting for volume protection.
	defaultVolumeProtectionProbe = appsv1alpha1.ClusterDefinitionProbe{
		PeriodSeconds:    60,
		TimeoutSeconds:   5,
		FailureThreshold: 3,
	}
)

func buildProbeContainers(reqCtx intctrlutil.RequestCtx, component *SynthesizedComponent) error {
	container, err := buildProbeContainer()
	if err != nil {
		return err
	}

	probeContainers := []corev1.Container{}
	componentProbes := component.Probes
	if componentProbes == nil {
		return nil
	}
	reqCtx.Log.V(3).Info("probe", "settings", componentProbes)
	probeSvcHTTPPort := viper.GetInt32("PROBE_SERVICE_HTTP_PORT")
	probeSvcGRPCPort := viper.GetInt32("PROBE_SERVICE_GRPC_PORT")
	availablePorts, err := getAvailableContainerPorts(component.PodSpec.Containers, []int32{probeSvcHTTPPort, probeSvcGRPCPort})
	probeSvcHTTPPort = availablePorts[0]
	probeSvcGRPCPort = availablePorts[1]
	if err != nil {
		reqCtx.Log.Info("get probe container port failed", "error", err)
		return err
	}

	if componentProbes.RoleProbe != nil {
		roleChangedContainer := container.DeepCopy()
		buildRoleProbeContainer(component, roleChangedContainer, componentProbes.RoleProbe, int(probeSvcHTTPPort))
		probeContainers = append(probeContainers, *roleChangedContainer)
	}

	if componentProbes.StatusProbe != nil {
		statusProbeContainer := container.DeepCopy()
		buildStatusProbeContainer(component.CharacterType, statusProbeContainer, componentProbes.StatusProbe, int(probeSvcHTTPPort))
		probeContainers = append(probeContainers, *statusProbeContainer)
	}

	if componentProbes.RunningProbe != nil {
		runningProbeContainer := container.DeepCopy()
		buildRunningProbeContainer(component.CharacterType, runningProbeContainer, componentProbes.RunningProbe, int(probeSvcHTTPPort))
		probeContainers = append(probeContainers, *runningProbeContainer)
	}

	if volumeProtectionEnabled(component) {
		c := container.DeepCopy()
		buildVolumeProtectionProbeContainer(component.CharacterType, c, int(probeSvcHTTPPort))
		probeContainers = append(probeContainers, *c)
	}

	if len(probeContainers) >= 1 {
		container := &probeContainers[0]
		buildProbeServiceContainer(component, container, int(probeSvcHTTPPort), int(probeSvcGRPCPort))
	}

	reqCtx.Log.V(1).Info("probe", "containers", probeContainers)
	component.PodSpec.Containers = append(component.PodSpec.Containers, probeContainers...)
	return nil
}

func buildProbeContainer() (*corev1.Container, error) {
	cueFS, _ := debme.FS(cueTemplates, "cue")

	cueTpl, err := intctrlutil.NewCUETplFromBytes(cueFS.ReadFile("probe_template.cue"))
	if err != nil {
		return nil, err
	}
	cueValue := intctrlutil.NewCUEBuilder(*cueTpl)
	probeContainerByte, err := cueValue.Lookup("probeContainer")
	if err != nil {
		return nil, err
	}
	container := &corev1.Container{}
	if err = json.Unmarshal(probeContainerByte, container); err != nil {
		return nil, err
	}
	return container, nil
}

func buildProbeServiceContainer(component *SynthesizedComponent, container *corev1.Container, probeSvcHTTPPort int, probeSvcGRPCPort int) {
	container.Image = viper.GetString(constant.KBToolsImage)
	container.ImagePullPolicy = corev1.PullPolicy(viper.GetString(constant.KBImagePullPolicy))
	logLevel := viper.GetString("PROBE_SERVICE_LOG_LEVEL")
	container.Command = []string{"probe", "--app-id", "batch-sdk",
		"--dapr-http-port", strconv.Itoa(probeSvcHTTPPort),
		"--dapr-grpc-port", strconv.Itoa(probeSvcGRPCPort),
		"--log-level", logLevel,
		"--config", "/config/probe/config.yaml",
		"--components-path", "/config/probe/components"}

	if len(component.PodSpec.Containers) > 0 && len(component.PodSpec.Containers[0].Ports) > 0 {
		mainContainer := component.PodSpec.Containers[0]
		port := mainContainer.Ports[0]
		dbPort := port.ContainerPort
		container.Env = append(container.Env, corev1.EnvVar{
			Name:      constant.KBEnvServicePort,
			Value:     strconv.Itoa(int(dbPort)),
			ValueFrom: nil,
		})
	}

	roles := getComponentRoles(component)
	rolesJSON, _ := json.Marshal(roles)
	container.Env = append(container.Env, corev1.EnvVar{
		Name:      constant.KBEnvServiceRoles,
		Value:     string(rolesJSON),
		ValueFrom: nil,
	})

	container.Env = append(container.Env, corev1.EnvVar{
		Name:      constant.KBEnvCharacterType,
		Value:     component.CharacterType,
		ValueFrom: nil,
	})

	container.Env = append(container.Env, corev1.EnvVar{
		Name:      constant.KBEnvWorkloadType,
		Value:     string(component.WorkloadType),
		ValueFrom: nil,
	})

	container.Ports = []corev1.ContainerPort{
		{
			ContainerPort: int32(probeSvcHTTPPort),
			Name:          constant.ProbeHTTPPortName,
			Protocol:      "TCP",
		},
		{
			ContainerPort: int32(probeSvcGRPCPort),
			Name:          constant.ProbeGRPCPortName,
			Protocol:      "TCP",
		}}

	// pass the volume protection spec to probe container through env.
	if volumeProtectionEnabled(component) {
		container.Env = append(container.Env, env4VolumeProtection(*component.VolumeProtection))
	}
}

func getComponentRoles(component *SynthesizedComponent) map[string]string {
	var roles = map[string]string{}
	if component.ConsensusSpec == nil {
		return roles
	}

	consensus := component.ConsensusSpec
	roles[strings.ToLower(consensus.Leader.Name)] = string(consensus.Leader.AccessMode)
	for _, follower := range consensus.Followers {
		roles[strings.ToLower(follower.Name)] = string(follower.AccessMode)
	}
	if consensus.Learner != nil {
		roles[strings.ToLower(consensus.Learner.Name)] = string(consensus.Learner.AccessMode)
	}
	return roles
}

func buildRoleProbeContainer(component *SynthesizedComponent, roleChangedContainer *corev1.Container,
	probeSetting *appsv1alpha1.ClusterDefinitionProbe, probeSvcHTTPPort int) {
	roleChangedContainer.Name = constant.RoleProbeContainerName
	probe := roleChangedContainer.ReadinessProbe
	bindingType := strings.ToLower(component.CharacterType)
	workloadType := component.WorkloadType
	httpGet := &corev1.HTTPGetAction{}
	httpGet.Path = fmt.Sprintf(checkRoleURIFormat, bindingType, workloadType)
	httpGet.Port = intstr.FromInt(probeSvcHTTPPort)
	probe.Exec = nil
	probe.HTTPGet = httpGet
	probe.PeriodSeconds = probeSetting.PeriodSeconds
	probe.TimeoutSeconds = probeSetting.TimeoutSeconds
	probe.FailureThreshold = probeSetting.FailureThreshold
	roleChangedContainer.StartupProbe.TCPSocket.Port = intstr.FromInt(probeSvcHTTPPort)
}

func buildStatusProbeContainer(characterType string, statusProbeContainer *corev1.Container,
	probeSetting *appsv1alpha1.ClusterDefinitionProbe, probeSvcHTTPPort int) {
	statusProbeContainer.Name = constant.StatusProbeContainerName
	probe := statusProbeContainer.ReadinessProbe
	httpGet := &corev1.HTTPGetAction{}
	httpGet.Path = fmt.Sprintf(checkStatusURIFormat, characterType)
	httpGet.Port = intstr.FromInt(probeSvcHTTPPort)
	probe.Exec = nil
	probe.HTTPGet = httpGet
	probe.PeriodSeconds = probeSetting.PeriodSeconds
	probe.TimeoutSeconds = probeSetting.TimeoutSeconds
	probe.FailureThreshold = probeSetting.FailureThreshold
	statusProbeContainer.StartupProbe.TCPSocket.Port = intstr.FromInt(probeSvcHTTPPort)
}

func buildRunningProbeContainer(characterType string, runningProbeContainer *corev1.Container,
	probeSetting *appsv1alpha1.ClusterDefinitionProbe, probeSvcHTTPPort int) {
	runningProbeContainer.Name = constant.RunningProbeContainerName
	probe := runningProbeContainer.ReadinessProbe
	httpGet := &corev1.HTTPGetAction{}
	httpGet.Path = fmt.Sprintf(checkRunningURIFormat, characterType)
	httpGet.Port = intstr.FromInt(probeSvcHTTPPort)
	probe.Exec = nil
	probe.HTTPGet = httpGet
	probe.PeriodSeconds = probeSetting.PeriodSeconds
	probe.TimeoutSeconds = probeSetting.TimeoutSeconds
	probe.FailureThreshold = probeSetting.FailureThreshold
	runningProbeContainer.StartupProbe.TCPSocket.Port = intstr.FromInt(probeSvcHTTPPort)
}

func volumeProtectionEnabled(component *SynthesizedComponent) bool {
	return component.VolumeProtection != nil
}

func buildVolumeProtectionProbeContainer(characterType string, c *corev1.Container, probeSvcHTTPPort int) {
	c.Name = constant.VolumeProtectionProbeContainerName
	probe := c.ReadinessProbe
	httpGet := &corev1.HTTPGetAction{}
	httpGet.Path = fmt.Sprintf(volumeProtectionURIFormat, characterType)
	httpGet.Port = intstr.FromInt(probeSvcHTTPPort)
	probe.Exec = nil
	probe.HTTPGet = httpGet
	probe.PeriodSeconds = defaultVolumeProtectionProbe.PeriodSeconds
	probe.TimeoutSeconds = defaultVolumeProtectionProbe.TimeoutSeconds
	probe.FailureThreshold = defaultVolumeProtectionProbe.FailureThreshold
	c.StartupProbe.TCPSocket.Port = intstr.FromInt(probeSvcHTTPPort)
}

func env4VolumeProtection(spec appsv1alpha1.VolumeProtectionSpec) corev1.EnvVar {
	value, err := json.Marshal(spec)
	if err != nil {
		panic(fmt.Sprintf("marshal volume protection spec error: %s", err.Error()))
	}
	return corev1.EnvVar{
		Name:  constant.KBEnvVolumeProtectionSpec,
		Value: string(value),
	}
}
