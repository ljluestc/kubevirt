/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright The KubeVirt Authors.
 */

package virtcontroller

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rhobs/operator-observability-toolkit/pkg/operatormetrics"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/log"
)

const (
	transTimeErrFmt = "Error encountered during VMI transition time histogram calculation: %v"
	transTimeFail   = "Failed to get a histogram for a VMI lifecycle transition times"
)

var (
	perfscaleMetrics = []operatormetrics.Metric{
		vmiPhaseTransition,
		vmiPhaseTransitionTimeFromCreation,
		vmiPhaseTransitionFromDeletion,
		vmiAgentConnectedTimeFromRunning,
	}

	vmiPhaseTransition = operatormetrics.NewHistogramVec(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vmi_phase_transition_time_seconds",
			Help: "Histogram of VM phase transitions duration between different phases in seconds.",
		},
		prometheus.HistogramOpts{
			Buckets: PhaseTransitionTimeBuckets(),
		},
		[]string{
			// phase of the vmi
			"phase",
			// last phase of the vmi
			"last_phase",
		},
	)

	vmiPhaseTransitionTimeFromCreation = operatormetrics.NewHistogramVec(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vmi_phase_transition_time_from_creation_seconds",
			Help: "Histogram of VM phase transitions duration from creation time in seconds.",
		},
		prometheus.HistogramOpts{
			Buckets: PhaseTransitionTimeBuckets(),
		},
		[]string{
			// phase of the vmi
			"phase",
		},
	)

	vmiPhaseTransitionFromDeletion = operatormetrics.NewHistogramVec(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vmi_phase_transition_time_from_deletion_seconds",
			Help: "Histogram of VM phase transitions duration from deletion time in seconds.",
		},
		prometheus.HistogramOpts{
			Buckets: PhaseTransitionTimeBuckets(),
		},
		[]string{
			// phase of the vmi
			"phase",
		},
	)

	vmiAgentConnectedTimeFromRunning = operatormetrics.NewHistogram(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vmi_agent_connected_time_from_running_seconds",
			Help: "Histogram of the duration from the VMI entering the Running phase until the guest agent first connects, in seconds. " +
				"Approximates guest boot time (firmware, bootloader, kernel and init) for VMIs running a guest agent.",
		},
		prometheus.HistogramOpts{
			Buckets: PhaseTransitionTimeBuckets(),
		},
	)
)

func AddVMIPhaseTransitionHandlers(informer cache.SharedIndexInformer) error {
	err := addVMIPhaseTransitionHandler(informer)
	if err != nil {
		return err
	}

	err = addVMIPhaseTransitionTimeFromCreationHandler(informer)
	if err != nil {
		return err
	}

	err = addVMIPhaseTransitionTimeFromDeletionHandler(informer)
	if err != nil {
		return err
	}

	err = addVMIAgentConnectedTimeFromRunningHandler(informer)
	if err != nil {
		return err
	}

	return nil
}

func addVMIPhaseTransitionHandler(informer cache.SharedIndexInformer) error {
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldVMI, newVMI interface{}) {
			updateVMIPhaseTransitionTime(oldVMI.(*v1.VirtualMachineInstance), newVMI.(*v1.VirtualMachineInstance))
		},
	})
	return err
}

func addVMIPhaseTransitionTimeFromCreationHandler(informer cache.SharedIndexInformer) error {
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldVMI, newVMI interface{}) {
			updateVMIPhaseTransitionTimeFromCreationTime(oldVMI.(*v1.VirtualMachineInstance), newVMI.(*v1.VirtualMachineInstance))
		},
	})
	return err
}

func addVMIPhaseTransitionTimeFromDeletionHandler(informer cache.SharedIndexInformer) error {
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldVMI, newVMI interface{}) {
			// User is deleting a VM. Record the time from the
			// deletionTimestamp to when the VMI enters the final phase
			updateVMIPhaseTransitionTimeFromDeletionTime(oldVMI.(*v1.VirtualMachineInstance), newVMI.(*v1.VirtualMachineInstance))
		},
	})
	return err
}

func addVMIAgentConnectedTimeFromRunningHandler(informer cache.SharedIndexInformer) error {
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldVMI, newVMI interface{}) {
			updateVMIAgentConnectedTimeFromRunning(oldVMI.(*v1.VirtualMachineInstance), newVMI.(*v1.VirtualMachineInstance))
		},
	})
	return err
}

func updateVMIPhaseTransitionTime(oldVMI, newVMI *v1.VirtualMachineInstance) {
	if oldVMI == nil || oldVMI.Status.Phase == newVMI.Status.Phase {
		return
	}

	diffSeconds, err := getVMITransitionTimeSeconds(false, false, oldVMI, newVMI)
	if err != nil {
		log.Log.V(logVerbosityDebug).Infof(transTimeErrFmt, err)
		return
	}

	labels := []string{string(newVMI.Status.Phase), string(oldVMI.Status.Phase)}
	histogram, err := vmiPhaseTransition.GetMetricWithLabelValues(labels...)
	if err != nil {
		log.Log.Reason(err).Error(transTimeFail)
		return
	}

	histogram.Observe(diffSeconds)
}

func updateVMIPhaseTransitionTimeFromCreationTime(oldVMI, newVMI *v1.VirtualMachineInstance) {
	if oldVMI == nil || oldVMI.Status.Phase == newVMI.Status.Phase {
		return
	}

	diffSeconds, err := getVMITransitionTimeSeconds(true, false, oldVMI, newVMI)
	if err != nil {
		log.Log.V(logVerbosityDebug).Infof(transTimeErrFmt, err)
		return
	}

	labels := []string{string(newVMI.Status.Phase)}
	histogram, err := vmiPhaseTransitionTimeFromCreation.GetMetricWithLabelValues(labels...)
	if err != nil {
		log.Log.Reason(err).Error(transTimeFail)
		return
	}

	histogram.Observe(diffSeconds)
}

func updateVMIPhaseTransitionTimeFromDeletionTime(oldVMI, newVMI *v1.VirtualMachineInstance) {
	if !newVMI.IsMarkedForDeletion() || !newVMI.IsFinal() {
		return
	}

	if oldVMI == nil || oldVMI.Status.Phase == newVMI.Status.Phase {
		return
	}

	diffSeconds, err := getVMITransitionTimeSeconds(false, true, oldVMI, newVMI)
	if err != nil {
		log.Log.V(logVerbosityDebug).Infof(transTimeErrFmt, err)
		return
	}

	labels := []string{string(newVMI.Status.Phase)}
	histogram, err := vmiPhaseTransitionFromDeletion.GetMetricWithLabelValues(labels...)
	if err != nil {
		log.Log.Reason(err).Error(transTimeFail)
		return
	}

	histogram.Observe(diffSeconds)
}

func updateVMIAgentConnectedTimeFromRunning(oldVMI, newVMI *v1.VirtualMachineInstance) {
	diffSeconds, err := getVMIAgentConnectedTimeFromRunningSeconds(oldVMI, newVMI)
	if err != nil {
		log.Log.V(logVerbosityDebug).Infof(transTimeErrFmt, err)
		return
	}
	if diffSeconds == nil {
		return
	}

	vmiAgentConnectedTimeFromRunning.Observe(*diffSeconds)
}

// getVMIAgentConnectedTimeFromRunningSeconds returns the time between the VMI
// entering the Running phase and the guest agent connecting for the first time.
// It returns nil when the update is not a first connection: the guest OS info,
// which is only reported by the agent and never cleared, is used to skip
// reconnections after guest reboots or migrations.
func getVMIAgentConnectedTimeFromRunningSeconds(oldVMI, newVMI *v1.VirtualMachineInstance) (*float64, error) {
	if oldVMI == nil || newVMI.Status.Phase != v1.Running || oldVMI.Status.GuestOSInfo.Name != "" {
		return nil, nil
	}

	if getAgentConnectedCondition(oldVMI) != nil {
		return nil, nil
	}
	agentCondition := getAgentConnectedCondition(newVMI)
	if agentCondition == nil {
		return nil, nil
	}

	var runningTime *metav1.Time
	for _, transitionTimestamp := range newVMI.Status.PhaseTransitionTimestamps {
		if transitionTimestamp.Phase == v1.Running {
			runningTime = transitionTimestamp.PhaseTransitionTimestamp.DeepCopy()
			break
		}
	}

	var connectedTime *metav1.Time
	if !agentCondition.LastProbeTime.IsZero() {
		connectedTime = agentCondition.LastProbeTime.DeepCopy()
	}

	diffSeconds, err := getTransitionTimeSeconds(runningTime, connectedTime)
	if err != nil {
		return nil, err
	}
	return &diffSeconds, nil
}

func getAgentConnectedCondition(vmi *v1.VirtualMachineInstance) *v1.VirtualMachineInstanceCondition {
	for i := range vmi.Status.Conditions {
		cond := &vmi.Status.Conditions[i]
		if cond.Type == v1.VirtualMachineInstanceAgentConnected && cond.Status == k8sv1.ConditionTrue {
			return cond
		}
	}
	return nil
}

func getOldTime(fromCreation, fromDeletion bool, oldVMI, newVMI *v1.VirtualMachineInstance) (*metav1.Time, error) {
	if fromCreation || oldVMI == nil || (oldVMI.Status.Phase == v1.VmPhaseUnset) {
		return newVMI.CreationTimestamp.DeepCopy(), nil
	}
	if fromDeletion && newVMI.IsMarkedForDeletion() {
		return newVMI.DeletionTimestamp.DeepCopy(), nil
	}
	if fromDeletion && !newVMI.IsMarkedForDeletion() {
		return nil, fmt.Errorf("missing deletion timestamp")
	}
	return nil, nil
}

func getVMITransitionTimeSeconds(fromCreation, fromDeletion bool, oldVMI, newVMI *v1.VirtualMachineInstance) (float64, error) {
	oldTime, err := getOldTime(fromCreation, fromDeletion, oldVMI, newVMI)
	if err != nil {
		return 0.0, err
	}

	var newTime *metav1.Time
	for _, transitionTimestamp := range newVMI.Status.PhaseTransitionTimestamps {
		if newTime == nil && transitionTimestamp.Phase == newVMI.Status.Phase {
			newTime = transitionTimestamp.PhaseTransitionTimestamp.DeepCopy()
		} else if oldTime == nil && oldVMI != nil && transitionTimestamp.Phase == oldVMI.Status.Phase {
			oldTime = transitionTimestamp.PhaseTransitionTimestamp.DeepCopy()
		} else if oldTime != nil && newTime != nil {
			break
		}
	}

	return getTransitionTimeSeconds(oldTime, newTime)
}
