package awsplacementgroup

import (
	"context"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var _ = Describe("Reconciler", func() {
	var mgrCancel context.CancelFunc
	var mgrDone chan struct{}
	var fakeRecorder *record.FakeRecorder

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "pg-test-"}}
		mgr, err := manager.New(cfg, manager.Options{MetricsBindAddress: "0", Namespace: namespace.Name})
		Expect(err).ToNot(HaveOccurred())

		r := Reconciler{
			Client: mgr.GetClient(),
			Log:    log.Log,
		}
		Expect(r.SetupWithManager(mgr, controller.Options{})).To(Succeed())

		fakeRecorder = record.NewFakeRecorder(1)
		r.recorder = fakeRecorder

		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		By("Starting the manager")
		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(ctx)
		mgrDone = make(chan struct{})

		go func() {
			defer GinkgoRecover()
			defer close(mgrDone)

			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()

	})

	AfterEach(func() {
		By("Stopping the manager")
		mgrCancel()
		// Wait for the mgrDone to be closed, which will happen once the mgr has stopped
		<-mgrDone
		// TODO DELETE all Placements to succeed
	})
})
