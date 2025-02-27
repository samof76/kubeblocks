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

package operations

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	cfgcore "github.com/apecloud/kubeblocks/internal/configuration"
	"github.com/apecloud/kubeblocks/internal/constant"
	"github.com/apecloud/kubeblocks/internal/controller/plan"
)

type reconfiguringResult struct {
	failed               bool
	noFormatFilesUpdated bool
	configPatch          *cfgcore.ConfigPatchInfo
	lastAppliedConfigs   map[string]string
	err                  error
}

// updateConfigConfigmapResource merges parameters of the config into the configmap, and verifies final configuration file.
func updateConfigConfigmapResource(config appsv1alpha1.Configuration,
	configSpec appsv1alpha1.ComponentConfigSpec,
	cmKey client.ObjectKey,
	ctx context.Context,
	cli client.Client,
	opsCrName string) reconfiguringResult {
	var (
		cm = &corev1.ConfigMap{}
		cc = &appsv1alpha1.ConfigConstraint{}

		err    error
		newCfg map[string]string
	)

	if err := cli.Get(ctx, cmKey, cm); err != nil {
		return makeReconfiguringResult(err)
	}
	if err := cli.Get(ctx, client.ObjectKey{
		Namespace: configSpec.Namespace,
		Name:      configSpec.ConfigConstraintRef,
	}, cc); err != nil {
		return makeReconfiguringResult(err)
	}

	updatedFiles := make(map[string]string, len(config.Keys))
	updatedParams := make([]cfgcore.ParamPairs, 0, len(config.Keys))
	for _, key := range config.Keys {
		if key.FileContent != "" {
			updatedFiles[key.Key] = key.FileContent
			continue
		}
		if len(key.Parameters) > 0 {
			updatedParams = append(updatedParams, cfgcore.ParamPairs{
				Key:           key.Key,
				UpdatedParams: fromKeyValuePair(key.Parameters)})
		}
	}

	if newCfg, err = mergeUpdatedParams(cm.Data, updatedFiles, updatedParams, cc.Spec, configSpec); err != nil {
		return makeReconfiguringResult(err, withFailed(true))
	}
	configPatch, restart, err := cfgcore.CreateConfigPatch(cm.Data, newCfg, cc.Spec.FormatterConfig.Format, configSpec.Keys, len(updatedFiles) != 0)
	if err != nil {
		return makeReconfiguringResult(err)
	}
	if !restart && !configPatch.IsModify {
		return makeReconfiguringResult(nil, withReturned(newCfg, configPatch))
	}
	return makeReconfiguringResult(
		syncConfigmap(cm, newCfg, cli, ctx, opsCrName, configSpec, &cc.Spec, config.Policy),
		withReturned(newCfg, configPatch),
		withNoFormatFilesUpdated(restart))
}

func mergeUpdatedParams(base map[string]string,
	updatedFiles map[string]string,
	updatedParams []cfgcore.ParamPairs,
	cc appsv1alpha1.ConfigConstraintSpec,
	tpl appsv1alpha1.ComponentConfigSpec) (map[string]string, error) {
	updatedConfig := base

	// merge updated files into configmap
	if len(updatedFiles) != 0 {
		updatedConfig = cfgcore.MergeUpdatedConfig(base, updatedFiles)
	}
	if len(updatedParams) == 0 {
		return updatedConfig, nil
	}
	return cfgcore.MergeAndValidateConfigs(cc, updatedConfig, tpl.Keys, updatedParams)
}

func syncConfigmap(cmObj *corev1.ConfigMap, newCfg map[string]string, cli client.Client, ctx context.Context, opsCrName string, configSpec appsv1alpha1.ComponentConfigSpec, cc *appsv1alpha1.ConfigConstraintSpec, policy *appsv1alpha1.UpgradePolicy) error {
	patch := client.MergeFrom(cmObj.DeepCopy())
	cmObj.Data = newCfg
	if cmObj.Annotations == nil {
		cmObj.Annotations = make(map[string]string)
	}
	if policy != nil {
		cmObj.Annotations[constant.UpgradePolicyAnnotationKey] = string(*policy)
	}
	cmObj.Annotations[constant.LastAppliedOpsCRAnnotationKey] = opsCrName
	cfgcore.SetParametersUpdateSource(cmObj, constant.ReconfigureUserSource)
	if err := plan.SyncEnvConfigmap(configSpec, cmObj, cc, cli, ctx); err != nil {
		return err
	}
	return cli.Patch(ctx, cmObj, patch)
}

func fromKeyValuePair(parameters []appsv1alpha1.ParameterPair) map[string]interface{} {
	m := make(map[string]interface{}, len(parameters))
	for _, param := range parameters {
		if param.Value != nil {
			m[param.Key] = *param.Value
		} else {
			m[param.Key] = nil
		}
	}
	return m
}

func withFailed(failed bool) func(result *reconfiguringResult) {
	return func(result *reconfiguringResult) {
		result.failed = failed
	}
}

func withReturned(configs map[string]string, patch *cfgcore.ConfigPatchInfo) func(result *reconfiguringResult) {
	return func(result *reconfiguringResult) {
		result.lastAppliedConfigs = configs
		result.configPatch = patch
	}
}

func withNoFormatFilesUpdated(changed bool) func(result *reconfiguringResult) {
	return func(result *reconfiguringResult) {
		result.noFormatFilesUpdated = changed
	}
}

func makeReconfiguringResult(err error, ops ...func(*reconfiguringResult)) reconfiguringResult {
	result := reconfiguringResult{
		failed: false,
		err:    err,
	}
	for _, o := range ops {
		o(&result)
	}
	return result
}

func getConfigSpecName(configSpec []appsv1alpha1.ComponentConfigSpec) []string {
	names := make([]string, len(configSpec))
	for i, spec := range configSpec {
		names[i] = spec.Name
	}
	return names
}

func constructReconfiguringConditions(result reconfiguringResult, resource *OpsResource, configSpec *appsv1alpha1.ComponentConfigSpec) *metav1.Condition {
	if result.configPatch.IsModify || result.noFormatFilesUpdated {
		return appsv1alpha1.NewReconfigureRunningCondition(
			resource.OpsRequest,
			appsv1alpha1.ReasonReconfigureMerged,
			configSpec.Name,
			formatConfigPatchToMessage(result.configPatch, nil))
	}
	return appsv1alpha1.NewReconfigureRunningCondition(
		resource.OpsRequest,
		appsv1alpha1.ReasonReconfigureNoChanged,
		configSpec.Name,
		formatConfigPatchToMessage(result.configPatch, nil))
}

func i2sMap(config map[string]interface{}) map[string]string {
	if len(config) == 0 {
		return nil
	}
	m := make(map[string]string, len(config))
	for key, value := range config {
		data, _ := json.Marshal(value)
		m[key] = string(data)
	}
	return m
}

func b2sMap(config map[string][]byte) map[string]string {
	if len(config) == 0 {
		return nil
	}
	m := make(map[string]string, len(config))
	for key, value := range config {
		m[key] = string(value)
	}
	return m
}

func processMergedFailed(resource *OpsResource, isInvalid bool, err error) error {
	if !isInvalid {
		return cfgcore.WrapError(err, "failed to update param!")
	}

	// if failed to validate configure, set opsRequest to failed and return
	failedCondition := appsv1alpha1.NewReconfigureFailedCondition(resource.OpsRequest, err)
	resource.OpsRequest.SetStatusCondition(*failedCondition)
	return nil
}

func formatConfigPatchToMessage(configPatch *cfgcore.ConfigPatchInfo, execStatus *cfgcore.PolicyExecStatus) string {
	policyName := ""
	if execStatus != nil {
		policyName = fmt.Sprintf("updated policy: <%s>, ", execStatus.PolicyName)
	}
	return fmt.Sprintf("%supdated: %s, added: %s, deleted:%s",
		policyName,
		configPatch.UpdateConfig,
		configPatch.AddConfig,
		configPatch.DeleteConfig)
}

func getClusterVersionResource(cvName string, cv *appsv1alpha1.ClusterVersion, cli client.Client, ctx context.Context) error {
	if cvName == "" {
		return nil
	}
	clusterVersionKey := client.ObjectKey{
		Namespace: "",
		Name:      cvName,
	}
	if err := cli.Get(ctx, clusterVersionKey, cv); err != nil {
		return cfgcore.WrapError(err, "failed to get clusterversion[%s]", cvName)
	}
	return nil
}
