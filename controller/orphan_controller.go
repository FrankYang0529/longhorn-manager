package controller

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"

	lhns "github.com/longhorn/go-common-libs/ns"

	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/engineapi"
	"github.com/longhorn/longhorn-manager/types"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
)

type OrphanController struct {
	*baseController

	// which namespace controller is running with
	namespace string
	// use as the OwnerID of the controller
	controllerID string

	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	ds *datastore.DataStore

	cacheSyncs []cache.InformerSynced
}

func NewOrphanController(
	logger logrus.FieldLogger,
	ds *datastore.DataStore,
	scheme *runtime.Scheme,
	kubeClient clientset.Interface,
	controllerID string,
	namespace string) (*OrphanController, error) {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{
		Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events(""),
	})

	oc := &OrphanController{
		baseController: newBaseController("longhorn-orphan", logger),

		namespace:    namespace,
		controllerID: controllerID,

		ds: ds,

		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "longhorn-orphan-controller"}),
	}

	var err error
	if _, err = ds.OrphanInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    oc.enqueueOrphan,
		UpdateFunc: func(old, cur interface{}) { oc.enqueueOrphan(cur) },
		DeleteFunc: oc.enqueueOrphan,
	}); err != nil {
		return nil, err
	}
	oc.cacheSyncs = append(oc.cacheSyncs, ds.OrphanInformer.HasSynced)

	if _, err = ds.NodeInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(cur interface{}) { oc.enqueueForLonghornNode(cur) },
		UpdateFunc: func(old, cur interface{}) { oc.enqueueForLonghornNode(cur) },
		DeleteFunc: func(cur interface{}) { oc.enqueueForLonghornNode(cur) },
	}, 0); err != nil {
		return nil, err
	}
	oc.cacheSyncs = append(oc.cacheSyncs, ds.NodeInformer.HasSynced)

	return oc, nil
}

func (oc *OrphanController) enqueueOrphan(obj interface{}) {
	key, err := controller.KeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to get key for object %#v: %v", obj, err))
		return
	}

	oc.queue.Add(key)
}

func (oc *OrphanController) enqueueForLonghornNode(obj interface{}) {
	node, ok := obj.(*longhorn.Node)
	if !ok {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", obj))
			return
		}
		// use the last known state, to enqueue, dependent objects
		node, ok = deletedState.Obj.(*longhorn.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("DeletedFinalStateUnknown contained invalid object: %#v", deletedState.Obj))
			return
		}
	}

	orphans, err := oc.ds.ListOrphansByNodeRO(node.Name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to list orphans on node %v since %v", node.Name, err))
		return
	}

	for _, orphan := range orphans {
		oc.enqueueOrphan(orphan)
	}
}

func (oc *OrphanController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer oc.queue.ShutDown()

	oc.logger.Info("Starting Longhorn Orphan controller")
	defer oc.logger.Info("Shut down Longhorn Orphan controller")

	if !cache.WaitForNamedCacheSync(oc.name, stopCh, oc.cacheSyncs...) {
		return
	}
	for i := 0; i < workers; i++ {
		go wait.Until(oc.worker, time.Second, stopCh)
	}
	<-stopCh
}

func (oc *OrphanController) worker() {
	for oc.processNextWorkItem() {
	}
}

func (oc *OrphanController) processNextWorkItem() bool {
	key, quit := oc.queue.Get()
	if quit {
		return false
	}
	defer oc.queue.Done(key)
	err := oc.syncOrphan(key.(string))
	oc.handleErr(err, key)
	return true
}

func (oc *OrphanController) handleErr(err error, key interface{}) {
	if err == nil {
		oc.queue.Forget(key)
		return
	}

	log := oc.logger.WithField("orphan", key)
	if oc.queue.NumRequeues(key) < maxRetries {
		handleReconcileErrorLogging(log, err, "Failed to sync Longhorn orphan")
		oc.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	handleReconcileErrorLogging(log, err, "Dropping Longhorn orphan out of the queue")
	oc.queue.Forget(key)
}

func (oc *OrphanController) syncOrphan(key string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to sync orphan %v", key)
	}()

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if namespace != oc.namespace {
		return nil
	}
	return oc.reconcile(name)
}

func (oc *OrphanController) reconcile(orphanName string) (err error) {
	orphan, err := oc.ds.GetOrphan(orphanName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	log := getLoggerForOrphan(oc.logger, orphan)

	if !oc.isResponsibleFor(orphan) {
		return nil
	}

	if orphan.Status.OwnerID != oc.controllerID {
		orphan.Status.OwnerID = oc.controllerID
		orphan, err = oc.ds.UpdateOrphanStatus(orphan)
		if err != nil {
			// we don't mind others coming first
			if apierrors.IsConflict(errors.Cause(err)) {
				return nil
			}
			return err
		}
		log.Infof("Orphan got new owner %v", oc.controllerID)
	}

	if !orphan.DeletionTimestamp.IsZero() {
		defer func() {
			if err == nil {
				err = oc.ds.RemoveFinalizerForOrphan(orphan)
			}
		}()

		return oc.cleanupOrphanedData(orphan)
	}

	existingOrphan := orphan.DeepCopy()
	defer func() {
		if err != nil {
			return
		}
		if reflect.DeepEqual(existingOrphan.Status, orphan.Status) {
			return
		}
		if _, err := oc.ds.UpdateOrphanStatus(orphan); err != nil && apierrors.IsConflict(errors.Cause(err)) {
			log.WithError(err).Debugf("Requeue %v due to conflict", orphanName)
			oc.enqueueOrphan(orphan)
		}
	}()

	if err := oc.updateConditions(orphan); err != nil {
		return errors.Wrapf(err, "failed to update conditions for orphan %v", orphan.Name)
	}

	return nil
}

func getLoggerForOrphan(logger logrus.FieldLogger, orphan *longhorn.Orphan) *logrus.Entry {
	return logger.WithFields(
		logrus.Fields{
			"orphan": orphan.Name,
		},
	)
}

func (oc *OrphanController) isResponsibleFor(orphan *longhorn.Orphan) bool {
	return isControllerResponsibleFor(oc.controllerID, oc.ds, orphan.Name, orphan.Spec.NodeID, orphan.Status.OwnerID)
}

func (oc *OrphanController) cleanupOrphanedData(orphan *longhorn.Orphan) (err error) {
	log := getLoggerForOrphan(oc.logger, orphan)

	defer func() {
		if err == nil {
			return
		}

		err = errors.Wrapf(err, "failed to delete orphan %v data", orphan.Name)
		orphan.Status.Conditions = types.SetCondition(orphan.Status.Conditions,
			longhorn.OrphanConditionTypeError, longhorn.ConditionStatusTrue, "", err.Error())
	}()

	// Make sure if the orphan nodeID and controller ID are same.
	// If NO, just delete the orphan resource object and don't touch the data.
	if orphan.Spec.NodeID != oc.controllerID {
		log.Infof("Orphan nodeID %v is different from controllerID %v, so just delete the orphan resource object",
			orphan.Name, oc.controllerID)
		return nil
	}

	if types.GetCondition(orphan.Status.Conditions, longhorn.OrphanConditionTypeDataCleanable).Status !=
		longhorn.ConditionStatusTrue {
		log.Infof("Only delete orphan %v resource object and do not delete the orphaned data", orphan.Name)
		return nil
	}

	switch orphan.Spec.Type {
	case longhorn.OrphanTypeReplica:
		err = oc.deleteOrphanedReplica(orphan)
	default:
		err = fmt.Errorf("unknown orphan type %v", orphan.Spec.Type)
	}

	if err == nil || (err != nil && apierrors.IsNotFound(err)) {
		return nil
	}

	return err
}

func (oc *OrphanController) deleteOrphanedReplica(orphan *longhorn.Orphan) error {
	oc.logger.Infof("Deleting orphan %v replica data store %v in disk %v on node %v",
		orphan.Name, orphan.Spec.Parameters[longhorn.OrphanDataName],
		orphan.Spec.Parameters[longhorn.OrphanDiskPath], orphan.Status.OwnerID)

	diskType, ok := orphan.Spec.Parameters[longhorn.OrphanDiskType]
	if !ok {
		return fmt.Errorf("failed to get disk type for orphan %v", orphan.Name)
	}

	switch longhorn.DiskType(diskType) {
	case longhorn.DiskTypeFilesystem:
		diskPath := orphan.Spec.Parameters[longhorn.OrphanDiskPath]
		replicaDirectoryName := orphan.Spec.Parameters[longhorn.OrphanDataName]
		err := lhns.DeletePath(filepath.Join(diskPath, "replicas", replicaDirectoryName))
		return errors.Wrapf(err, "failed to delete orphan replica directory %v in disk %v", replicaDirectoryName, diskPath)
	case longhorn.DiskTypeBlock:
		return oc.DeleteV2ReplicaInstance(orphan.Spec.Parameters[longhorn.OrphanDiskName], orphan.Spec.Parameters[longhorn.OrphanDiskUUID], "", orphan.Spec.Parameters[longhorn.OrphanDataName])
	default:
		return fmt.Errorf("unknown disk type %v for orphan %v", diskType, orphan.Name)
	}
}

func (oc *OrphanController) DeleteV2ReplicaInstance(diskName, diskUUID, diskDriver, replicaInstanceName string) (err error) {
	logrus.Infof("Deleting SPDK replica instance %v on disk %v on node %v", replicaInstanceName, diskUUID, oc.controllerID)

	defer func() {
		err = errors.Wrapf(err, "cannot delete v2 replica instance %v", replicaInstanceName)
	}()

	im, err := oc.ds.GetRunningInstanceManagerByNodeRO(oc.controllerID, longhorn.DataEngineTypeV2)
	if err != nil {
		return errors.Wrapf(err, "failed to get running instance manager for node %v for deleting v2 replica instance %v", oc.controllerID, replicaInstanceName)
	}

	c, err := engineapi.NewDiskServiceClient(im, oc.logger)
	if err != nil {
		return err
	}
	defer c.Close()

	err = c.DiskReplicaInstanceDelete(string(longhorn.DiskTypeBlock), diskName, diskUUID, diskDriver, replicaInstanceName)
	if err != nil && !types.ErrorIsNotFound(err) {
		return err
	}

	return nil
}

func (oc *OrphanController) updateConditions(orphan *longhorn.Orphan) error {
	if err := oc.updateDataCleanableCondition(orphan); err != nil {
		return err
	}

	if types.GetCondition(orphan.Status.Conditions, longhorn.OrphanConditionTypeError).Status != longhorn.ConditionStatusTrue {
		orphan.Status.Conditions = types.SetCondition(orphan.Status.Conditions, longhorn.OrphanConditionTypeError, longhorn.ConditionStatusFalse, "", "")
	}

	return nil
}

// If the DataCleanable condition status is false, then the deletion of the orphan CRD won't delete the on-disk data
func (oc *OrphanController) updateDataCleanableCondition(orphan *longhorn.Orphan) (err error) {
	reason := ""

	defer func() {
		if err != nil {
			return
		}

		status := longhorn.ConditionStatusTrue
		if reason != "" {
			status = longhorn.ConditionStatusFalse
		}

		orphan.Status.Conditions = types.SetCondition(orphan.Status.Conditions, longhorn.OrphanConditionTypeDataCleanable, status, reason, "")
	}()

	isUnavailable, err := oc.ds.IsNodeDownOrDeletedOrMissingManager(orphan.Spec.NodeID)
	if err != nil {
		return errors.Wrapf(err, "failed to check node down or missing manager for node %v", orphan.Spec.NodeID)
	}
	if isUnavailable {
		reason = longhorn.OrphanConditionTypeDataCleanableReasonNodeUnavailable
		return nil
	}

	node, err := oc.ds.GetNode(orphan.Spec.NodeID)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get node %v", orphan.Spec.NodeID)
		}
		reason = longhorn.OrphanConditionTypeDataCleanableReasonNodeUnavailable
		return nil
	}

	if node.Spec.EvictionRequested {
		reason = longhorn.OrphanConditionTypeDataCleanableReasonNodeEvicted
		return nil
	}

	if orphan.Spec.Type == longhorn.OrphanTypeReplica {
		reason = oc.checkOrphanedReplicaDataCleanable(node, orphan)
	}

	return nil
}

func (oc *OrphanController) checkOrphanedReplicaDataCleanable(node *longhorn.Node, orphan *longhorn.Orphan) string {
	diskName, err := oc.ds.GetReadyDisk(node.Name, orphan.Spec.Parameters[longhorn.OrphanDiskUUID])
	if err != nil {
		if strings.Contains(err.Error(), "cannot find the ready disk") {
			return longhorn.OrphanConditionTypeDataCleanableReasonDiskInvalid
		}

		return ""
	}

	disk := node.Spec.Disks[diskName]

	if diskName != orphan.Spec.Parameters[longhorn.OrphanDiskName] ||
		disk.Path != orphan.Spec.Parameters[longhorn.OrphanDiskPath] {
		return longhorn.OrphanConditionTypeDataCleanableReasonDiskChanged
	}

	if disk.EvictionRequested {
		return longhorn.OrphanConditionTypeDataCleanableReasonDiskEvicted
	}

	return ""
}
