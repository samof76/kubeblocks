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

package controllerutil

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/internal/constant"
	viper "github.com/apecloud/kubeblocks/internal/viperx"
)

// GetUncachedObjects returns a list of K8s objects, for these object types,
// and their list types, client.Reader will read directly from the API server instead
// of the cache, which may not be up-to-date.
// see sigs.k8s.io/controller-runtime/pkg/client/split.go to understand how client
// works with this UncachedObjects filter.
func GetUncachedObjects() []client.Object {
	// client-side read cache reduces the number of requests processed in the API server,
	// which is good for performance. However, it can sometimes lead to obscure issues,
	// most notably lacking read-after-write consistency, i.e. reading a value immediately
	// after updating it may miss to see the changes.
	// while in most cases this problem can be mitigated by retrying later in an idempotent
	// manner, there are some cases where it cannot, for example if a decision is to be made
	// that has side-effect operations such as returning an error message to the user
	// (in webhook) or deleting certain resources (in controllerutil.HandleCRDeletion).
	// additionally, retry loops cause unnecessary delays when reconciliations are processed.
	// for the sake of performance, now only the objects created by the end-user is listed here,
	// to solve the two problems mentioned above.
	// consider carefully before adding new objects to this list.
	return []client.Object{
		// avoid to cache potential large data objects
		&corev1.ConfigMap{},
		&corev1.Secret{},
		&appsv1alpha1.Cluster{},
	}
}

// Event is wrapper for Recorder.Event, if Recorder is nil, then it's no-op.
func (r *RequestCtx) Event(object runtime.Object, eventtype, reason, message string) {
	if r == nil || r.Recorder == nil {
		return
	}
	r.Recorder.Event(object, eventtype, reason, message)
}

// Eventf is wrapper for Recorder.Eventf, if Recorder is nil, then it's no-op.
func (r *RequestCtx) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	if r == nil || r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(object, eventtype, reason, messageFmt, args...)
}

// UpdateCtxValue updates Context value, returns parent Context.
func (r *RequestCtx) UpdateCtxValue(key, val any) context.Context {
	p := r.Ctx
	r.Ctx = context.WithValue(r.Ctx, key, val)
	return p
}

// WithValue returns a copy of parent in which the value associated with key is
// val.
func (r *RequestCtx) WithValue(key, val any) context.Context {
	return context.WithValue(r.Ctx, key, val)
}

// IsRSMEnabled enables rsm by default.
// respect the feature gate if set, keep the ability to disable it.
func IsRSMEnabled() bool {
	if viper.IsSet(constant.FeatureGateReplicatedStateMachine) {
		return viper.GetBool(constant.FeatureGateReplicatedStateMachine)
	}
	return true
}
