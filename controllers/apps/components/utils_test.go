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

package components

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/internal/constant"
	"github.com/apecloud/kubeblocks/internal/controller/builder"
	"github.com/apecloud/kubeblocks/internal/controller/component"
	"github.com/apecloud/kubeblocks/internal/controller/graph"
	"github.com/apecloud/kubeblocks/internal/controller/types"
	intctrlutil "github.com/apecloud/kubeblocks/internal/controllerutil"
	"github.com/apecloud/kubeblocks/internal/generics"
	testapps "github.com/apecloud/kubeblocks/internal/testutil/apps"
	testk8s "github.com/apecloud/kubeblocks/internal/testutil/k8s"
)

func TestIsFailedOrAbnormal(t *testing.T) {
	if !IsFailedOrAbnormal(appsv1alpha1.AbnormalClusterCompPhase) {
		t.Error("isAbnormal should be true")
	}
}

func TestIsProbeTimeout(t *testing.T) {
	podsReadyTime := &metav1.Time{Time: time.Now().Add(-10 * time.Minute)}
	compDef := &appsv1alpha1.ClusterComponentDefinition{
		Probes: &appsv1alpha1.ClusterDefinitionProbes{
			RoleProbe:                      &appsv1alpha1.ClusterDefinitionProbe{},
			RoleProbeTimeoutAfterPodsReady: appsv1alpha1.DefaultRoleProbeTimeoutAfterPodsReady,
		},
	}
	if !isProbeTimeout(compDef.Probes, podsReadyTime) {
		t.Error("probe timed out should be true")
	}
}

func TestGetComponentPhase(t *testing.T) {
	var (
		isFailed   = true
		isAbnormal = true
	)
	getComponentPhase := func(isFailed, isAbnormal bool) appsv1alpha1.ClusterComponentPhase {
		var componentPhase appsv1alpha1.ClusterComponentPhase
		if isFailed {
			componentPhase = appsv1alpha1.FailedClusterCompPhase
		} else if isAbnormal {
			componentPhase = appsv1alpha1.AbnormalClusterCompPhase
		}
		return componentPhase
	}
	status := getComponentPhase(isFailed, isAbnormal)
	if status != appsv1alpha1.FailedClusterCompPhase {
		t.Error("function getComponentPhase should return Failed")
	}
	isFailed = false
	status = getComponentPhase(isFailed, isAbnormal)
	if status != appsv1alpha1.AbnormalClusterCompPhase {
		t.Error("function getComponentPhase should return Abnormal")
	}
	isAbnormal = false
	status = getComponentPhase(isFailed, isAbnormal)
	if status != "" {
		t.Error(`function getComponentPhase should return ""`)
	}
}

func TestGetPhaseWithNoAvailableReplicas(t *testing.T) {
	status := getPhaseWithNoAvailableReplicas(int32(0))
	if status != "" {
		t.Error(`function getComponentPhase should return ""`)
	}
	status = getPhaseWithNoAvailableReplicas(int32(2))
	if status != appsv1alpha1.FailedClusterCompPhase {
		t.Error(`function getComponentPhase should return "Failed"`)
	}
}

func TestAvailableReplicasAreConsistent(t *testing.T) {
	isConsistent := availableReplicasAreConsistent(int32(1), int32(1), int32(1))
	if !isConsistent {
		t.Error(`function getComponentPhase should return "true"`)
	}
	isConsistent = availableReplicasAreConsistent(int32(1), int32(2), int32(1))
	if isConsistent {
		t.Error(`function getComponentPhase should return "false"`)
	}
}

func TestGetCompPhaseByConditions(t *testing.T) {
	existLatestRevisionFailedPod := true
	primaryReplicaIsReady := true
	phase := getCompPhaseByConditions(existLatestRevisionFailedPod, primaryReplicaIsReady, int32(1), int32(1), int32(1))
	if phase != "" {
		t.Error(`function getComponentPhase should return ""`)
	}
	phase = getCompPhaseByConditions(existLatestRevisionFailedPod, primaryReplicaIsReady, int32(2), int32(1), int32(1))
	if phase != appsv1alpha1.AbnormalClusterCompPhase {
		t.Error(`function getComponentPhase should return "Abnormal"`)
	}
	primaryReplicaIsReady = false
	phase = getCompPhaseByConditions(existLatestRevisionFailedPod, primaryReplicaIsReady, int32(2), int32(1), int32(1))
	if phase != appsv1alpha1.FailedClusterCompPhase {
		t.Error(`function getComponentPhase should return "Failed"`)
	}
	existLatestRevisionFailedPod = false
	phase = getCompPhaseByConditions(existLatestRevisionFailedPod, primaryReplicaIsReady, int32(2), int32(1), int32(1))
	if phase != "" {
		t.Error(`function getComponentPhase should return ""`)
	}
}

var _ = Describe("Consensus Component", func() {
	var (
		randomStr          = testCtx.GetRandomStr()
		clusterDefName     = "mysql-clusterdef-" + randomStr
		clusterVersionName = "mysql-clusterversion-" + randomStr
		clusterName        = "mysql-" + randomStr
	)

	const (
		consensusCompDefRef = "consensus"
		consensusCompName   = "consensus"
		statelessCompName   = "stateless"
	)

	cleanAll := func() {
		// must wait until resources deleted and no longer exist before the testcases start,
		// otherwise if later it needs to create some new resource objects with the same name,
		// in race conditions, it will find the existence of old objects, resulting failure to
		// create the new objects.
		By("clean resources")
		// delete cluster(and all dependent sub-resources), clusterversion and clusterdef
		testapps.ClearClusterResources(&testCtx)

		// clear rest resources
		inNS := client.InNamespace(testCtx.DefaultNamespace)
		ml := client.HasLabels{testCtx.TestObjLabelKey}
		// namespaced resources
		testapps.ClearResources(&testCtx, generics.StatefulSetSignature, inNS, ml)
		testapps.ClearResources(&testCtx, generics.PodSignature, inNS, ml, client.GracePeriodSeconds(0))
	}

	BeforeEach(cleanAll)

	AfterEach(cleanAll)

	Context("Consensus Component test", func() {
		It("Consensus Component test", func() {
			By(" init cluster, statefulSet, pods")
			_, _, cluster := testapps.InitClusterWithHybridComps(&testCtx, clusterDefName,
				clusterVersionName, clusterName, statelessCompName, "stateful", consensusCompName)
			sts := testapps.MockConsensusComponentStatefulSet(&testCtx, clusterName, consensusCompName)
			testapps.MockStatelessComponentDeploy(&testCtx, clusterName, statelessCompName)
			_ = testapps.MockConsensusComponentPods(&testCtx, sts, clusterName, consensusCompName)

			By("test GetComponentDefByCluster function")
			componentDef, _ := appsv1alpha1.GetComponentDefByCluster(ctx, k8sClient, *cluster, consensusCompDefRef)
			Expect(componentDef != nil).Should(BeTrue())

			By("test GetClusterByObject function")
			newCluster, _ := GetClusterByObject(ctx, k8sClient, sts)
			Expect(newCluster != nil).Should(BeTrue())

			By("test consensusSet initClusterComponentStatusIfNeed function")
			err := initClusterComponentStatusIfNeed(cluster, consensusCompName, componentDef.WorkloadType)
			Expect(err).Should(Succeed())
			Expect(cluster.Status.Components[consensusCompName].ConsensusSetStatus).ShouldNot(BeNil())
			Expect(cluster.Status.Components[consensusCompName].ConsensusSetStatus.Leader.Pod).Should(Equal(constant.ComponentStatusDefaultPodName))

			By("test replicationSet initClusterComponentStatusIfNeed function")
			componentDef.WorkloadType = appsv1alpha1.Replication
			err = initClusterComponentStatusIfNeed(cluster, consensusCompName, componentDef.WorkloadType)
			Expect(err).Should(Succeed())
			Expect(cluster.Status.Components[consensusCompName].ReplicationSetStatus).ShouldNot(BeNil())
			Expect(cluster.Status.Components[consensusCompName].ReplicationSetStatus.Primary.Pod).Should(Equal(constant.ComponentStatusDefaultPodName))

			By("test getObjectListByComponentName function")
			stsList := &appsv1.StatefulSetList{}
			_ = getObjectListByComponentName(ctx, k8sClient, *cluster, stsList, consensusCompName)
			Expect(len(stsList.Items) > 0).Should(BeTrue())

			By("test getObjectListByCustomLabels function")
			stsList = &appsv1.StatefulSetList{}
			matchLabel := getComponentMatchLabels(cluster.Name, consensusCompName)
			_ = getObjectListByCustomLabels(ctx, k8sClient, *cluster, stsList, client.MatchingLabels(matchLabel))
			Expect(len(stsList.Items) > 0).Should(BeTrue())

			By("test getClusterComponentSpecByName function")
			clusterComp := getClusterComponentSpecByName(*cluster, consensusCompName)
			Expect(clusterComp).ShouldNot(BeNil())

			By("test GetComponentStsMinReadySeconds")
			minReadySeconds, _ := GetComponentWorkloadMinReadySeconds(ctx, k8sClient, *cluster,
				appsv1alpha1.Stateless, statelessCompName)
			Expect(minReadySeconds).To(Equal(int32(10)))
			minReadySeconds, _ = GetComponentWorkloadMinReadySeconds(ctx, k8sClient, *cluster,
				appsv1alpha1.Consensus, statelessCompName)
			Expect(minReadySeconds).To(Equal(int32(0)))

			By("test getCompRelatedObjectList function")
			stsList = &appsv1.StatefulSetList{}
			podList, _ := getCompRelatedObjectList(ctx, k8sClient, *cluster, consensusCompName, stsList)
			Expect(len(stsList.Items) > 0 && len(podList.Items) > 0).Should(BeTrue())

			By("test GetComponentInfoByPod function")
			componentName, componentDef, err := GetComponentInfoByPod(ctx, k8sClient, *cluster, &podList.Items[0])
			Expect(err).Should(Succeed())
			Expect(componentName).Should(Equal(consensusCompName))
			Expect(componentDef).ShouldNot(BeNil())
			By("test GetComponentInfoByPod function when Pod is nil")
			_, _, err = GetComponentInfoByPod(ctx, k8sClient, *cluster, nil)
			Expect(err).ShouldNot(Succeed())
			By("test GetComponentInfoByPod function when Pod component label is nil")
			podNoLabel := &podList.Items[0]
			delete(podNoLabel.Labels, constant.KBAppComponentLabelKey)
			_, _, err = GetComponentInfoByPod(ctx, k8sClient, *cluster, podNoLabel)
			Expect(err).ShouldNot(Succeed())

			By("test getComponentPhaseWhenPodsNotReady function")
			consensusComp := cluster.Spec.GetComponentByName(consensusCompName)
			checkExistFailedPodOfLatestRevision := func(pod *corev1.Pod, workload metav1.Object) bool {
				sts := workload.(*appsv1.StatefulSet)
				return !intctrlutil.PodIsReady(pod) && intctrlutil.PodIsControlledByLatestRevision(pod, sts)
			}
			// component phase should be Failed when available replicas is 0
			phase := getComponentPhaseWhenPodsNotReady(podList, sts, consensusComp.Replicas,
				sts.Status.AvailableReplicas, checkExistFailedPodOfLatestRevision)
			Expect(phase).Should(Equal(appsv1alpha1.FailedClusterCompPhase))

			// mock available replicas to component replicas
			Expect(testapps.ChangeObjStatus(&testCtx, sts, func() {
				testk8s.MockStatefulSetReady(sts)
			})).Should(Succeed())
			phase = getComponentPhaseWhenPodsNotReady(podList, sts, consensusComp.Replicas,
				sts.Status.AvailableReplicas, checkExistFailedPodOfLatestRevision)
			Expect(len(phase) == 0).Should(BeTrue())

			// mock component is abnormal
			pod := &podList.Items[0]
			Expect(testapps.ChangeObjStatus(&testCtx, pod, func() {
				pod.Status.Conditions = nil
			})).Should(Succeed())
			Expect(testapps.ChangeObjStatus(&testCtx, sts, func() {
				sts.Status.AvailableReplicas = *sts.Spec.Replicas - 1
			})).Should(Succeed())
			phase = getComponentPhaseWhenPodsNotReady(podList, sts, consensusComp.Replicas,
				sts.Status.AvailableReplicas, checkExistFailedPodOfLatestRevision)
			Expect(phase).Should(Equal(appsv1alpha1.AbnormalClusterCompPhase))

		})

		It("test GetComponentInfoByPod with no cluster componentSpec", func() {
			_, _, cluster := testapps.InitClusterWithHybridComps(&testCtx, clusterDefName,
				clusterVersionName, clusterName, statelessCompName, "stateful", consensusCompName)
			By("set componentSpec to nil")
			cluster.Spec.ComponentSpecs = nil
			pod := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constant.KBAppComponentLabelKey: consensusCompName,
					},
				},
			}
			componentName, componentDef, err := GetComponentInfoByPod(ctx, k8sClient, *cluster, &pod)
			Expect(err).Should(Succeed())
			Expect(componentName).Should(Equal(consensusCompName))
			Expect(componentDef).ShouldNot(BeNil())
		})
	})

	Context("Custom Label test", func() {
		Context("parseCustomLabelPattern func", func() {
			It("should parse pattern well", func() {
				pattern := "v1/Pod"
				gvk, err := parseCustomLabelPattern(pattern)
				Expect(err).Should(BeNil())
				Expect(gvk.Group).Should(BeEmpty())
				Expect(gvk.Version).Should(Equal("v1"))
				Expect(gvk.Kind).Should(Equal("Pod"))
				pattern = "apps/v1/StatefulSet"
				gvk, err = parseCustomLabelPattern(pattern)
				Expect(err).Should(BeNil())
				Expect(gvk.Group).Should(Equal("apps"))
				Expect(gvk.Version).Should(Equal("v1"))
				Expect(gvk.Kind).Should(Equal("StatefulSet"))
			})
		})

		Context("updateCustomLabelToObjs func", func() {
			It("should update label well", func() {
				resource := &appsv1alpha1.GVKResource{GVK: "v1/Pod"}
				customLabelSpec := appsv1alpha1.CustomLabelSpec{
					Key:       "custom-label-key",
					Value:     "$(KB_CLUSTER_NAME)-$(KB_COMP_NAME)",
					Resources: []appsv1alpha1.GVKResource{*resource},
				}
				pod := builder.NewPodBuilder("foo", "bar").GetObject()
				clusterName, uid, componentName := "foo", "1234-5678", "workload"
				err := updateCustomLabelToObjs(clusterName, uid, componentName, []appsv1alpha1.CustomLabelSpec{customLabelSpec}, []client.Object{pod})
				Expect(err).Should(BeNil())
				Expect(pod.Labels).ShouldNot(BeNil())
				Expect(pod.Labels[customLabelSpec.Key]).Should(Equal(fmt.Sprintf("%s-%s", clusterName, componentName)))
			})
		})

		Context("updateCustomLabelToPods func", func() {
			It("should work well", func() {
				_, _, cluster := testapps.InitClusterWithHybridComps(&testCtx, clusterDefName,
					clusterVersionName, clusterName, statelessCompName, "stateful", consensusCompName)
				sts := testapps.MockConsensusComponentStatefulSet(&testCtx, clusterName, consensusCompName)
				pods := testapps.MockConsensusComponentPods(&testCtx, sts, clusterName, consensusCompName)
				resource := &appsv1alpha1.GVKResource{GVK: "v1/Pod"}
				customLabelSpec := appsv1alpha1.CustomLabelSpec{
					Key:       "custom-label-key",
					Value:     "$(KB_CLUSTER_NAME)-$(KB_COMP_NAME)",
					Resources: []appsv1alpha1.GVKResource{*resource},
				}
				comp := &component.SynthesizedComponent{
					Name:             consensusCompName,
					CustomLabelSpecs: []appsv1alpha1.CustomLabelSpec{customLabelSpec},
				}
				dag := graph.NewDAG()
				dag.AddVertex(&types.LifecycleVertex{Obj: pods[0], Action: types.ActionUpdatePtr()})
				Expect(updateCustomLabelToPods(testCtx.Ctx, k8sClient, cluster, comp, dag)).Should(Succeed())
				podVertices := types.FindAll[*corev1.Pod](dag)
				Expect(podVertices).Should(HaveLen(3))
				for _, vertex := range podVertices {
					v, _ := vertex.(*types.LifecycleVertex)
					Expect(v.Obj.GetLabels()).ShouldNot(BeNil())
					Expect(v.Obj.GetLabels()[customLabelSpec.Key]).Should(Equal(fmt.Sprintf("%s-%s", clusterName, comp.Name)))
				}
			})
		})
	})
})

var _ = Describe("Component utils test", func() {
	Context("test mergeServiceAnnotations", func() {
		It("should merge annotations from original that not exist in target to final result", func() {
			originalKey := "only-existing-in-original"
			targetKey := "only-existing-in-target"
			updatedKey := "updated-in-target"
			originalAnnotations := map[string]string{
				originalKey: "true",
				updatedKey:  "false",
			}
			targetAnnotations := map[string]string{
				targetKey:  "true",
				updatedKey: "true",
			}
			mergeAnnotations(originalAnnotations, &targetAnnotations)
			Expect(targetAnnotations[targetKey]).ShouldNot(BeEmpty())
			Expect(targetAnnotations[originalKey]).ShouldNot(BeEmpty())
			Expect(targetAnnotations[updatedKey]).Should(Equal("true"))
			By("merging with target being nil")
			var nilAnnotations map[string]string
			mergeAnnotations(originalAnnotations, &nilAnnotations)
			Expect(nilAnnotations).ShouldNot(BeNil())
		})

		It("test sync pod spec default values set by k8s", func() {
			var (
				clusterName = "cluster"
				compName    = "component"
				podName     = "pod"
				role        = "leader"
				mode        = "ReadWrite"
			)
			pod := testapps.MockConsensusComponentStsPod(&testCtx, nil, clusterName, compName, podName, role, mode)
			ppod := testapps.NewPodFactory(testCtx.DefaultNamespace, "pod").
				SetOwnerReferences("apps/v1", constant.StatefulSetKind, nil).
				AddAppInstanceLabel(clusterName).
				AddAppComponentLabel(compName).
				AddAppManangedByLabel().
				AddRoleLabel(role).
				AddConsensusSetAccessModeLabel(mode).
				AddControllerRevisionHashLabel("").
				AddContainer(corev1.Container{
					Name:  testapps.DefaultMySQLContainerName,
					Image: testapps.ApeCloudMySQLImage,
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/hello",
								Port: intstr.FromInt(1024),
							},
						},
						TimeoutSeconds:   1,
						PeriodSeconds:    1,
						FailureThreshold: 1,
					},
					StartupProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{
								Port: intstr.FromInt(1024),
							},
						},
					},
				}).
				GetObject()
			resolvePodSpecDefaultFields(pod.Spec, &ppod.Spec)
			Expect(reflect.DeepEqual(pod.Spec, ppod.Spec)).Should(BeTrue())
		})
	})
})
