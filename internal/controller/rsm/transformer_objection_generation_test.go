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

package rsm

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/golang/mock/gomock"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/apecloud/kubeblocks/internal/controller/builder"
)

var _ = Describe("object generation transformer test.", func() {
	BeforeEach(func() {
		rsm = builder.NewReplicatedStateMachineBuilder(namespace, name).
			SetUID(uid).
			AddMatchLabelsInMap(selectors).
			SetServiceName(headlessSvcName).
			SetRoles(roles).
			SetService(service).
			SetCredential(credential).
			SetTemplate(template).
			SetProbeActions(observeActions).
			GetObject()

		transCtx = &rsmTransformContext{
			Context:       ctx,
			Client:        graphCli,
			EventRecorder: nil,
			Logger:        logger,
			rsmOrig:       rsm.DeepCopy(),
			rsm:           rsm,
		}

		transformer = &ObjectGenerationTransformer{}
	})

	Context("Transform function", func() {
		It("should work well", func() {
			sts := builder.NewStatefulSetBuilder(namespace, name).GetObject()
			headlessSvc := builder.NewHeadlessServiceBuilder(name, getHeadlessSvcName(*rsm)).GetObject()
			svc := builder.NewServiceBuilder(name, name).GetObject()
			env := builder.NewConfigMapBuilder(name, name+"-env").GetObject()
			k8sMock.EXPECT().
				List(gomock.Any(), &apps.StatefulSetList{}, gomock.Any()).
				DoAndReturn(func(_ context.Context, list *apps.StatefulSetList, _ ...client.ListOption) error {
					return nil
				}).Times(1)
			k8sMock.EXPECT().
				List(gomock.Any(), &corev1.ServiceList{}, gomock.Any()).
				DoAndReturn(func(_ context.Context, list *corev1.ServiceList, _ ...client.ListOption) error {
					return nil
				}).Times(1)
			k8sMock.EXPECT().
				List(gomock.Any(), &corev1.ConfigMapList{}, gomock.Any()).
				DoAndReturn(func(_ context.Context, list *corev1.ConfigMapList, _ ...client.ListOption) error {
					return nil
				}).Times(1)

			dagExpected := mockDAG()
			graphCli.Create(dagExpected, sts)
			graphCli.Create(dagExpected, headlessSvc)
			graphCli.Create(dagExpected, svc)
			graphCli.Create(dagExpected, env)
			graphCli.DependOn(dagExpected, sts, headlessSvc, svc, env)

			// do Transform
			dag := mockDAG()
			Expect(transformer.Transform(transCtx, dag)).Should(Succeed())

			// compare DAGs
			Expect(dag.Equals(dagExpected, less)).Should(BeTrue())
		})
	})
})
