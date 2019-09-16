/*
Copyright 2018 The Knative Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package revision

import (
	"context"
	"fmt"

	perrors "github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/logging/logkey"
	"knative.dev/serving/pkg/apis/autoscaling"
	"knative.dev/serving/pkg/apis/serving/v1alpha1"
	"knative.dev/serving/pkg/reconciler/revision/resources"
	resourcenames "knative.dev/serving/pkg/reconciler/revision/resources/names"
)

func (c *Reconciler) reconcileDeployment(ctx context.Context, rev *v1alpha1.Revision) error {
	ns := rev.Namespace
	deploymentName := resourcenames.Deployment(rev)
	logger := logging.FromContext(ctx).With(zap.String(logkey.Deployment, deploymentName))

	deployment, err := c.deploymentLister.Deployments(ns).Get(deploymentName)
	if apierrs.IsNotFound(err) {
		// Deployment does not exist. Create it.
		rev.Status.MarkDeploying("Deploying")
		deployment, err = c.createDeployment(ctx, rev)
		if err != nil {
			return perrors.Wrapf(err, "failed to create deployment %q", deploymentName)
		}
		logger.Infof("Created deployment %q", deploymentName)
	} else if err != nil {
		return perrors.Wrapf(err, "failed to get deployment %q", deploymentName)
	} else if !metav1.IsControlledBy(deployment, rev) {
		// Surface an error in the revision's status, and return an error.
		rev.Status.MarkResourceNotOwned("Deployment", deploymentName)
		return fmt.Errorf("revision: %q does not own Deployment: %q", rev.Name, deploymentName)
	} else {
		// The deployment exists, but make sure that it has the shape that we expect.
		deployment, err = c.checkAndUpdateDeployment(ctx, rev, deployment)
		if err != nil {
			return perrors.Wrapf(err, "failed to update deployment %q", deploymentName)
		}

		// Now that we have a Deployment, determine whether there is any relevant
		// status to surface in the Revision.
		//
		// TODO(jonjohnsonjr): Should we check Generation != ObservedGeneration?
		// The autoscaler mutates the deployment pretty often, which would cause us
		// to flip back and forth between Ready and Unknown every time we scale up
		// or down.
		if !rev.Status.IsActivationRequired() {
			rev.Status.PropagateDeploymentStatus(&deployment.Status)
		}
	}

	// If a container keeps crashing (no active pods in the deployment although we want some)
	if *deployment.Spec.Replicas > 0 && deployment.Status.AvailableReplicas == 0 {
		pods, err := c.KubeClientSet.CoreV1().Pods(ns).List(metav1.ListOptions{LabelSelector: metav1.FormatLabelSelector(deployment.Spec.Selector)})
		if err != nil {
			logger.Errorw("Error getting pods", zap.Error(err))
		} else if len(pods.Items) > 0 {
			// Arbitrarily grab the very first pod, as they all should be crashing
			pod := pods.Items[0]

			// Update the revision status if pod cannot be scheduled(possibly resource constraints)
			// If pod cannot be scheduled then we expect the container status to be empty.
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
					rev.Status.MarkResourcesUnavailable(cond.Reason, cond.Message)
					break
				}
			}

			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == rev.Spec.GetContainer().Name {
					if t := status.LastTerminationState.Terminated; t != nil {
						logger.Infof("%s marking exiting with: %d/%s", rev.Name, t.ExitCode, t.Message)
						rev.Status.MarkContainerExiting(t.ExitCode, t.Message)
					} else if w := status.State.Waiting; w != nil && hasDeploymentTimedOut(deployment) {
						logger.Infof("%s marking resources unavailable with: %s: %s", rev.Name, w.Reason, w.Message)
						rev.Status.MarkResourcesUnavailable(w.Reason, w.Message)
					}
					break
				}
			}
		}
	}

	return nil
}

func (c *Reconciler) reconcileImageCache(ctx context.Context, rev *v1alpha1.Revision) error {
	logger := logging.FromContext(ctx)

	ns := rev.Namespace
	imageName := resourcenames.ImageCache(rev)
	_, err := c.imageLister.Images(ns).Get(imageName)
	if apierrs.IsNotFound(err) {
		_, err := c.createImageCache(ctx, rev)
		if err != nil {
			return perrors.Wrapf(err, "failed to create image cache %q", imageName)
		}
		logger.Infof("Created image cache %q", imageName)
	} else if err != nil {
		return perrors.Wrapf(err, "failed to get image cache %q", imageName)
	}

	return nil
}

func (c *Reconciler) reconcilePA(ctx context.Context, rev *v1alpha1.Revision) error {
	ns := rev.Namespace
	paName := resourcenames.PA(rev)
	logger := logging.FromContext(ctx)
	logger.Info("Reconciling PA: ", paName)

	pa, err := c.podAutoscalerLister.PodAutoscalers(ns).Get(paName)
	if apierrs.IsNotFound(err) {
		// PA does not exist. Create it.
		pa, err = c.createPA(ctx, rev)
		if err != nil {
			return perrors.Wrapf(err, "failed to create PA %q", paName)
		}
		/* TODO #5403 once after confirmation of number of classes autoscaler can support than this condition can be removed and handled through validation.
		Currently when given class value is other than (KPA and HPA) /metric value will be empty
		so based on the empty value of metric we can decide whether provided class is valid or invalid*/
		if value, ok := pa.Annotations[autoscaling.MetricAnnotationKey]; ok && value == "" {
			rev.Status.MarkResourcesUnavailable("Create PA failed", "Failed to create PA because of invalid class "+pa.Annotations[autoscaling.ClassAnnotationKey])
			return fmt.Errorf("Failed to create PA because of invalid class %s", pa.Annotations[autoscaling.ClassAnnotationKey])
		}
		logger.Info("Created PA: ", paName)
	} else if err != nil {
		return perrors.Wrapf(err, "failed to get PA %q", paName)
	} else if !metav1.IsControlledBy(pa, rev) {
		// Surface an error in the revision's status, and return an error.
		rev.Status.MarkResourceNotOwned("PodAutoscaler", paName)
		return fmt.Errorf("revision: %q does not own PodAutoscaler: %q", rev.Name, paName)
	}

	// Perhaps tha PA spec changed underneath ourselves?
	// We no longer require immutability, so need to reconcile PA each time.
	tmpl := resources.MakePA(rev)
	if !equality.Semantic.DeepEqual(tmpl.Spec, pa.Spec) {
		logger.Infof("PA %s needs reconciliation", pa.Name)

		want := pa.DeepCopy()
		want.Spec = tmpl.Spec
		if pa, err = c.ServingClientSet.AutoscalingV1alpha1().PodAutoscalers(pa.Namespace).Update(want); err != nil {
			return perrors.Wrapf(err, "failed to update PA %q", paName)
		}
	}

	rev.Status.PropagateAutoscalerStatus(&pa.Status)
	return nil
}

func hasDeploymentTimedOut(deployment *appsv1.Deployment) bool {
	// as per https://kubernetes.io/docs/concepts/workloads/controllers/deployment
	for _, cond := range deployment.Status.Conditions {
		// Look for Deployment with status False
		if cond.Status != corev1.ConditionFalse {
			continue
		}
		// with Type Progressing and Reason Timeout
		// TODO(arvtiwar): hard coding "ProgressDeadlineExceeded" to avoid import kubernetes/kubernetes
		if cond.Type == appsv1.DeploymentProgressing && cond.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}
