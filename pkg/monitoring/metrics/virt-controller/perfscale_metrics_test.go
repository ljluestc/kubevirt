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
 *
 */

package virtcontroller

import (
	"time"

	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "kubevirt.io/api/core/v1"
)

var _ = Describe("VMI phase transition time histogram", func() {
	Context("Transition Time calculations", func() {
		DescribeTable("time diff calculations", func(expectedVal float64, curPhase, oldPhase v1.VirtualMachineInstancePhase) {
			var oldVMI *v1.VirtualMachineInstance

			vmi := createVMISForPhaseTransitionTime(curPhase, oldPhase, expectedVal*1000, true)

			oldVMI = vmi.DeepCopy()
			oldVMI.Status.Phase = oldPhase

			diffSeconds, err := getVMITransitionTimeSeconds(false, false, oldVMI, vmi)
			Expect(err).ToNot(HaveOccurred())

			Expect(diffSeconds).To(Equal(expectedVal))
		},
			Entry("Time between running and scheduled", 5.0, v1.Running, v1.Scheduled),
			Entry("Time between running and scheduled using fraction of a second", .5, v1.Running, v1.Scheduled),
			Entry("Time between scheduled and scheduling", 2.0, v1.Scheduled, v1.Scheduling),
			Entry("Time between scheduling and pending", 1.0, v1.Scheduling, v1.Pending),
			Entry("Time between running and failed", 1.0, v1.Running, v1.Failed),
		)
	})

	Context("Time since Create/Delete calculations", func() {
		DescribeTable("time diff calculations", func(expectedVal float64, curPhase, oldPhase v1.VirtualMachineInstancePhase, creation bool) {
			var oldVMI *v1.VirtualMachineInstance

			vmi := createVMISForPhaseTransitionTime(curPhase, oldPhase, expectedVal*1000, true)
			if !creation {
				vmi.DeletionTimestamp = &vmi.CreationTimestamp
			}

			if oldPhase != "" {
				oldVMI = vmi.DeepCopy()
				oldVMI.Status.Phase = oldPhase
			}

			diffSeconds, err := getVMITransitionTimeSeconds(creation, !creation, oldVMI, vmi)
			Expect(err).ToNot(HaveOccurred())

			// Time since created or deleted timestamp
			// Value should be 2x expectedVal while time between Phases should be
			// 1x expectedVal because the measurement is creationtime -> oldphase -> newphase
			Expect(diffSeconds).To(Equal(2 * expectedVal))
		},
			Entry("Time between creation and pending", 3.0, v1.Pending, v1.VmPhaseUnset, true),
			Entry("Time between creation and running", 5.0, v1.Running, v1.Scheduled, true),
			Entry("Time between creation and scheduling using fraction of a second", .5, v1.Scheduling, v1.Scheduled, true),
			Entry("Time spent deleting a VMI that exited in a failed state", 5.0, v1.Failed, v1.Running, false),
			Entry("Time spent deleting a VMI that exited in a succeeded state", 5.0, v1.Succeeded, v1.Running, false),
		)
	})

	Context("Guest agent connected time from running calculations", func() {
		const bootSeconds = 7.5

		newRunningVMI := func() *v1.VirtualMachineInstance {
			return createVMISForPhaseTransitionTime(v1.Running, v1.Scheduled, 1000, true)
		}

		withAgentConnected := func(vmi *v1.VirtualMachineInstance, after time.Duration) *v1.VirtualMachineInstance {
			running := vmi.Status.PhaseTransitionTimestamps[0].PhaseTransitionTimestamp
			vmi = vmi.DeepCopy()
			vmi.Status.Conditions = append(vmi.Status.Conditions, v1.VirtualMachineInstanceCondition{
				Type:          v1.VirtualMachineInstanceAgentConnected,
				Status:        k8sv1.ConditionTrue,
				LastProbeTime: metav1.NewTime(running.Add(after)),
			})
			return vmi
		}

		It("should measure the time from Running until the agent first connects", func() {
			oldVMI := newRunningVMI()
			newVMI := withAgentConnected(oldVMI, time.Duration(bootSeconds*float64(time.Second)))

			diffSeconds, err := getVMIAgentConnectedTimeFromRunningSeconds(oldVMI, newVMI)
			Expect(err).ToNot(HaveOccurred())
			Expect(diffSeconds).ToNot(BeNil())
			Expect(*diffSeconds).To(Equal(bootSeconds))
		})

		DescribeTable("should not measure", func(mutate func(oldVMI, newVMI *v1.VirtualMachineInstance) (*v1.VirtualMachineInstance, *v1.VirtualMachineInstance)) {
			oldVMI := newRunningVMI()
			newVMI := withAgentConnected(oldVMI, 5*time.Second)
			oldVMI, newVMI = mutate(oldVMI, newVMI)

			diffSeconds, err := getVMIAgentConnectedTimeFromRunningSeconds(oldVMI, newVMI)
			Expect(err).ToNot(HaveOccurred())
			Expect(diffSeconds).To(BeNil())
		},
			Entry("when the agent is not connected", func(oldVMI, _ *v1.VirtualMachineInstance) (*v1.VirtualMachineInstance, *v1.VirtualMachineInstance) {
				return oldVMI, oldVMI.DeepCopy()
			}),
			Entry("when the agent was already connected", func(_, newVMI *v1.VirtualMachineInstance) (*v1.VirtualMachineInstance, *v1.VirtualMachineInstance) {
				return newVMI.DeepCopy(), newVMI
			}),
			Entry("when the agent reconnects after a guest reboot", func(oldVMI, newVMI *v1.VirtualMachineInstance) (*v1.VirtualMachineInstance, *v1.VirtualMachineInstance) {
				oldVMI.Status.GuestOSInfo.Name = "Fedora Linux"
				newVMI.Status.GuestOSInfo.Name = "Fedora Linux"
				return oldVMI, newVMI
			}),
			Entry("when the VMI is not running", func(oldVMI, newVMI *v1.VirtualMachineInstance) (*v1.VirtualMachineInstance, *v1.VirtualMachineInstance) {
				newVMI.Status.Phase = v1.Failed
				return oldVMI, newVMI
			}),
			Entry("when there is no old VMI", func(_, newVMI *v1.VirtualMachineInstance) (*v1.VirtualMachineInstance, *v1.VirtualMachineInstance) {
				return nil, newVMI
			}),
		)

		It("should fail when the Running transition timestamp is missing", func() {
			oldVMI := newRunningVMI()
			newVMI := withAgentConnected(oldVMI, 5*time.Second)
			newVMI.Status.PhaseTransitionTimestamps = nil

			_, err := getVMIAgentConnectedTimeFromRunningSeconds(oldVMI, newVMI)
			Expect(err).To(HaveOccurred())
		})

		It("should fail when the agent condition has no probe time", func() {
			oldVMI := newRunningVMI()
			newVMI := withAgentConnected(oldVMI, 5*time.Second)
			newVMI.Status.Conditions[0].LastProbeTime = metav1.Time{}

			_, err := getVMIAgentConnectedTimeFromRunningSeconds(oldVMI, newVMI)
			Expect(err).To(HaveOccurred())
		})
	})
})

func createVMISForPhaseTransitionTime(
	phase, oldPhase v1.VirtualMachineInstancePhase,
	offset float64,
	hasTransitionTime bool,
) *v1.VirtualMachineInstance {
	now := metav1.NewTime(time.Now())
	old := metav1.NewTime(now.Time.Add(-time.Duration(int64(offset)) * time.Millisecond))
	creation := metav1.NewTime(old.Time.Add(-time.Duration(int64(offset)) * time.Millisecond))

	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "test-ns",
			Name:              "testvmi",
			CreationTimestamp: creation,
		},
		Status: v1.VirtualMachineInstanceStatus{
			NodeName: "testNode",
			Phase:    phase,
		},
	}

	if hasTransitionTime {
		vmi.Status.PhaseTransitionTimestamps = append(vmi.Status.PhaseTransitionTimestamps, v1.VirtualMachineInstancePhaseTransitionTimestamp{
			Phase:                    phase,
			PhaseTransitionTimestamp: now,
		})
	}

	if oldPhase != "" {
		vmi.Status.PhaseTransitionTimestamps = append(vmi.Status.PhaseTransitionTimestamps, v1.VirtualMachineInstancePhaseTransitionTimestamp{
			Phase:                    oldPhase,
			PhaseTransitionTimestamp: old,
		})
	}

	return vmi
}
