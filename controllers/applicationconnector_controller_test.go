package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/kyma-project/application-connector-manager/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/pointer"
)

const (
	appGatewayDeploymentName             = "central-application-gateway"
	appConValidatorDeploymentName        = "central-application-connectivity-validator"
	appCompassRuntimeAgentDeploymentName = "compass-runtime-agent"
)

var _ = Describe("ApplicationConnector controller", func() {

	defaultTestTimeout := 60 * time.Second
	defaultAppCon := applicationConnector("test", "kyma-system", v1alpha1.ApplicationConnectorSpec{})

	Context("When creating fresh instance", func() {
		DescribeTable(
			"The application-connector is created properly with given specification",
			// the table function that will be executed for each entry
			testInstance,
			Entry("with default arguments", defaultTestTimeout, defaultAppCon),
		)
	})
})

func validateAppConState(ctx context.Context, expected State, key types.NamespacedName) error {
	state, err := getApplicationConnectorState(ctx, key)
	if err != nil {
		return err
	}
	if state != expected {
		return fmt.Errorf("invalid state")
	}
	return nil
}

func testInstance(t time.Duration, ac v1alpha1.ApplicationConnector) {
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()

	By(fmt.Sprintf("create namespace: %s", ac.Namespace))
	ns := namespace(ac.Namespace)
	Expect(k8sClient.Create(ctx, &ns)).To(Succeed())

	By(fmt.Sprintf("create application-connector instance: %s/%s", ac.Namespace, ac.Name))
	Expect(k8sClient.Create(ctx, &ac)).To(Succeed())

	instanceNsName := types.NamespacedName{Name: ac.Name, Namespace: ac.Namespace}
	// both deployments should not be ready, the CR status should be in
	// processing state
	Eventually(validateAppConState).
		WithArguments(ctx, StateProcessing, instanceNsName).
		WithPolling(time.Second).
		WithTimeout(t).
		Should(Succeed())

	By("simulate k8s reaction - update application-gateway deployment and create replica-set")
	appGatewayNsName := types.NamespacedName{Name: appGatewayDeploymentName, Namespace: ac.Namespace}
	Expect(simulateK8sDeploymentRdy(ctx, appGatewayNsName)).To(Succeed())

	// application-connectivity-validator deployments should not be ready, the CR status should be in
	// processing state
	Eventually(validateAppConState).
		WithArguments(ctx, StateProcessing, instanceNsName).
		WithPolling(time.Second).
		WithTimeout(t).
		Should(Succeed())

	By("simulate k8s reaction - update application-connectivity-validator deployment and create replica-set")
	appConValidatorDeploymentName := types.NamespacedName{Name: appConValidatorDeploymentName, Namespace: ac.Namespace}
	Expect(simulateK8sDeploymentRdy(ctx, appConValidatorDeploymentName)).To(Succeed())

	// both deployments should be ready, the CR status should be in
	// ready state
	Eventually(validateAppConState).
		WithArguments(ctx, StateReady, instanceNsName).
		WithPolling(time.Second).
		WithTimeout(t).
		Should(Succeed())
}

func namespace(name string) corev1.Namespace {
	return corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func applicationConnector(name, nsName string, spec v1alpha1.ApplicationConnectorSpec) v1alpha1.ApplicationConnector {
	return v1alpha1.ApplicationConnector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: nsName,
			Labels: map[string]string{
				"operator.kyma-project.io/kyma-name": "test",
			},
		},
		Spec: spec,
	}
}

func replicaSet(d appsv1.Deployment) appsv1.ReplicaSet {
	return appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-replica-set", d.Name),
			Namespace: d.Namespace,
			Labels: map[string]string{
				"app": d.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       d.Name,
					UID:        d.GetUID(),
					Controller: pointer.Bool(true),
				},
			},
		},
		// dummy values
		Spec: appsv1.ReplicaSetSpec{
			Replicas: pointer.Int32(1),
			Selector: d.Spec.Selector,
			Template: d.Spec.Template,
		},
	}
}

func simulateK8sDeploymentRdy(ctx context.Context, key types.NamespacedName) error {
	var deployment appsv1.Deployment
	if err := k8sClient.Get(ctx, key, &deployment); err != nil {
		return err
	}

	deployment.Status.Conditions = append(deployment.Status.Conditions, appsv1.DeploymentCondition{
		Type:    appsv1.DeploymentAvailable,
		Status:  corev1.ConditionTrue,
		Reason:  "test-reason",
		Message: "test-message",
	})
	deployment.Status.Replicas = 1

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return k8sClient.Status().Update(ctx, &deployment)
	})
	if err != nil {
		return err
	}

	rs := replicaSet(deployment)
	if err := k8sClient.Create(ctx, &rs); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	rs.Status.ReadyReplicas = 1
	rs.Status.Replicas = 1

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return k8sClient.Status().Update(ctx, &rs)
	})
}

func getApplicationConnectorState(ctx context.Context, key types.NamespacedName) (State, error) {
	var emptyState = State("")
	var connector v1alpha1.ApplicationConnector
	err := k8sClient.Get(ctx, key, &connector)
	if err != nil {
		return emptyState, err
	}
	return State(connector.Status.State), nil
}
