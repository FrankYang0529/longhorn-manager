package api

import (
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"github.com/rancher/go-rancher/api"
	"github.com/rancher/go-rancher/client"

	"github.com/rancher/longhorn-manager/types"
	"github.com/rancher/longhorn-manager/util"

	longhorn "github.com/rancher/longhorn-manager/k8s/pkg/apis/longhorn/v1alpha1"
)

func (s *Server) VolumeList(rw http.ResponseWriter, req *http.Request) (err error) {
	defer func() {
		err = errors.Wrap(err, "unable to list")
	}()

	apiContext := api.GetApiContext(req)

	resp, err := s.volumeList(apiContext)
	if err != nil {
		return err
	}

	apiContext.Write(resp)

	return nil
}

func (s *Server) volumeList(apiContext *api.ApiContext) (*client.GenericCollection, error) {
	resp := &client.GenericCollection{}

	volumes, err := s.m.ListSorted()
	if err != nil {
		return nil, err
	}

	for _, v := range volumes {
		controller, err := s.m.GetEngine(v.Name)
		if err != nil {
			return nil, err
		}
		replicas, err := s.m.GetReplicasSorted(v.Name)
		if err != nil {
			return nil, err
		}

		resp.Data = append(resp.Data, toVolumeResource(v, controller, replicas, apiContext))
	}
	resp.ResourceType = "volume"
	resp.CreateTypes = map[string]string{
		"volume": apiContext.UrlBuilder.Collection("volume"),
	}

	return resp, nil
}

func (s *Server) VolumeGet(rw http.ResponseWriter, req *http.Request) error {
	id := mux.Vars(req)["name"]
	return s.responseWithVolume(rw, req, id, nil)
}

func (s *Server) responseWithVolume(rw http.ResponseWriter, req *http.Request, id string, v *longhorn.Volume) error {
	var err error
	apiContext := api.GetApiContext(req)

	if v == nil {
		if id == "" {
			rw.WriteHeader(http.StatusNotFound)
			return nil
		}
		v, err = s.m.Get(id)
		if err != nil {
			return errors.Wrap(err, "unable to get volume")
		}
	}

	if v == nil {
		rw.WriteHeader(http.StatusNotFound)
		return nil
	}
	controller, err := s.m.GetEngine(id)
	if err != nil {
		return err
	}
	replicas, err := s.m.GetReplicasSorted(id)
	if err != nil {
		return err
	}

	apiContext.Write(toVolumeResource(v, controller, replicas, apiContext))
	return nil
}

func (s *Server) VolumeCreate(rw http.ResponseWriter, req *http.Request) error {
	var volume Volume
	apiContext := api.GetApiContext(req)

	if err := apiContext.Read(&volume); err != nil {
		return err
	}

	if volume.Frontend == "" {
		volume.Frontend = types.VolumeFrontendBlockDev
	}
	size, err := util.ConvertSize(volume.Size)
	if err != nil {
		return fmt.Errorf("fail to parse size %v", err)
	}
	v, err := s.m.Create(volume.Name, &types.VolumeSpec{
		Size:                size,
		Frontend:            volume.Frontend,
		FromBackup:          volume.FromBackup,
		NumberOfReplicas:    volume.NumberOfReplicas,
		StaleReplicaTimeout: volume.StaleReplicaTimeout,
	})
	if err != nil {
		return errors.Wrap(err, "unable to create volume")
	}
	return s.responseWithVolume(rw, req, "", v)
}

func (s *Server) VolumeDelete(rw http.ResponseWriter, req *http.Request) error {
	id := mux.Vars(req)["name"]

	if err := s.m.Delete(id); err != nil {
		return errors.Wrap(err, "unable to delete volume")
	}

	return nil
}

func (s *Server) VolumeAttach(rw http.ResponseWriter, req *http.Request) error {
	var input AttachInput

	apiContext := api.GetApiContext(req)
	if err := apiContext.Read(&input); err != nil {
		return err
	}

	id := mux.Vars(req)["name"]

	v, err := s.m.Attach(id, input.HostID)
	if err != nil {
		return err
	}

	return s.responseWithVolume(rw, req, "", v)
}

func (s *Server) VolumeDetach(rw http.ResponseWriter, req *http.Request) error {
	id := mux.Vars(req)["name"]

	v, err := s.m.Detach(id)
	if err != nil {
		return err
	}

	return s.responseWithVolume(rw, req, "", v)
}

func (s *Server) VolumeSalvage(rw http.ResponseWriter, req *http.Request) error {
	var input SalvageInput

	apiContext := api.GetApiContext(req)
	if err := apiContext.Read(&input); err != nil {
		return errors.Wrapf(err, "error read salvageInput")
	}

	id := mux.Vars(req)["name"]

	v, err := s.m.Salvage(id, input.Names)
	if err != nil {
		return errors.Wrap(err, "unable to salvage volume")
	}

	return s.responseWithVolume(rw, req, "", v)
}

func (s *Server) VolumeRecurringUpdate(rw http.ResponseWriter, req *http.Request) error {
	var input RecurringInput
	id := mux.Vars(req)["name"]

	apiContext := api.GetApiContext(req)
	if err := apiContext.Read(&input); err != nil {
		return errors.Wrapf(err, "error reading recurringInput")
	}

	v, err := s.m.UpdateRecurringJobs(id, input.Jobs)
	if err != nil {
		return errors.Wrapf(err, "unable to update recurring jobs for volume %v", id)
	}

	return s.responseWithVolume(rw, req, "", v)
}

func (s *Server) ReplicaRemove(rw http.ResponseWriter, req *http.Request) error {
	var input ReplicaRemoveInput

	apiContext := api.GetApiContext(req)
	if err := apiContext.Read(&input); err != nil {
		return errors.Wrapf(err, "error read replicaRemoveInput")
	}

	id := mux.Vars(req)["name"]

	if err := s.m.DeleteReplica(input.Name); err != nil {
		return errors.Wrap(err, "unable to remove replica")
	}

	return s.responseWithVolume(rw, req, id, nil)
}

func (s *Server) EngineUpgrade(rw http.ResponseWriter, req *http.Request) error {
	var input EngineUpgradeInput

	apiContext := api.GetApiContext(req)
	if err := apiContext.Read(&input); err != nil {
		return errors.Wrapf(err, "error read engineUpgradeInput")
	}

	id := mux.Vars(req)["name"]

	if err := s.m.EngineUpgrade(id, input.Image); err != nil {
		return errors.Wrap(err, "unable to upgrade engine")
	}

	return s.responseWithVolume(rw, req, id, nil)
}
