/*
** Copyright [2013-2016] [Megam Systems]
**
** Licensed under the Apache License, Version 2.0 (the "License");
** you may not use this file except in compliance with the License.
** You may obtain a copy of the License at
**
** http://www.apache.org/licenses/LICENSE-2.0
**
** Unless required by applicable law or agreed to in writing, software
** distributed under the License is distributed on an "AS IS" BASIS,
** WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
** See the License for the specific language governing permissions and
** limitations under the License.
 */

package one

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	log "github.com/Sirupsen/logrus"
	"github.com/megamsys/libgo/action"
	"github.com/megamsys/libgo/cmd"
	"github.com/megamsys/libgo/events"
	"github.com/megamsys/libgo/events/alerts"
	"github.com/megamsys/libgo/utils"
	constants "github.com/megamsys/libgo/utils"
	"github.com/megamsys/opennebula-go/api"
	"github.com/megamsys/vertice/carton"
	lb "github.com/megamsys/vertice/logbox"
	"github.com/megamsys/vertice/provision"
	"github.com/megamsys/vertice/provision/one/cluster"
	"github.com/megamsys/vertice/provision/one/machine"
	"github.com/megamsys/vertice/repository"
	"github.com/megamsys/vertice/router"
	_ "github.com/megamsys/vertice/router/route53"
)

var mainOneProvisioner *oneProvisioner

func init() {
	mainOneProvisioner = &oneProvisioner{}
	provision.Register("one", mainOneProvisioner)
}

type oneProvisioner struct {
	defaultImage string
	vcpuThrottle string
	cluster      *cluster.Cluster
	storage      cluster.Storage
}

type One struct {
	Enabled        bool     `json:"enabled" toml:"enabled"`
	Regions        []Region `json:"region" toml:"region"`
	Image          string   `json:"image" toml:"image"`
	VCPUPercentage string   `json:"vcpu_percentage" toml:"vcpu_percentage"`
	OneTemplate    string   `json:"one_template" toml:"one_template"`
}

type Region struct {
	OneZone        string    `json:"one_zone" toml:"one_zone"`
	OneEndPoint    string    `json:"one_endpoint" toml:"one_endpoint"`
	OneUserid      string    `json:"one_user" toml:"one_user"`
	OnePassword    string    `json:"one_password" toml:"one_password"`
	OneMasterKey   string    `json:"one_masterkey" toml:"one_masterkey"`
	OneTemplate    string    `json:"one_template" toml:"one_template"`
	Image          string    `json:"image" toml:"image"`
	VCPUPercentage string    `json:"vcpu_percentage" toml:"vcpu_percentage"`
	Certificate    string    `json:"certificate" toml:"certificate"`
	Clusters       []Cluster `json:"cluster" toml:"cluster"`
	CpuUnit        string    `json:"cpu_unit" toml:"cpu_unit"`
	MemoryUnit     string    `json:"memory_unit" toml:"memory_unit"`
	DiskUnit       string    `json:"disk_unit" toml:"disk_unit"`
}

type Cluster struct {
	Enabled       bool   `json:"enabled" toml:"enabled"`
	StorageType   string `json:"storage_hddtype" toml:"storage_hddtype"`
	VOneCloud     bool   `json:"vonecloud" toml:"vonecloud"`
	ClusterId     string `json:"cluster_id" toml:"cluster_id"`
	Vnet_pri_ipv4 string `json:"vnet_pri_ipv4" toml:"vnet_pri_ipv4"`
	Vnet_pub_ipv4 string `json:"vnet_pub_ipv4" toml:"vnet_pub_ipv4"`
	Vnet_pri_ipv6 string `json:"vnet_pri_ipv6" toml:"vnet_pri_ipv6"`
	Vnet_pub_ipv6 string `json:"vnet_pub_ipv6" toml:"vnet_pub_ipv6"`
}

func (p *oneProvisioner) Cluster() *cluster.Cluster {
	if p.cluster == nil {
		panic("✗ one cluster")
	}
	return p.cluster
}

func (p *oneProvisioner) String() string {
	if p.cluster == nil {
		return "✗ one cluster"
	}
	return "ready"
}

func (p *oneProvisioner) Initialize(m interface{}) error {
	return p.initOneCluster(m)
}

func (p *oneProvisioner) initOneCluster(i interface{}) error {
	var err error
	if p.storage == nil {
		p.storage, err = buildClusterStorage()
		if err != nil {
			return err
		}
	}

	if w, ok := i.(One); ok {
		var nodes []cluster.Node
		p.defaultImage = w.Image
		p.vcpuThrottle = w.VCPUPercentage
		for i := 0; i < len(w.Regions); i++ {
			m := w.Regions[i].toMap()
			c := w.Regions[i].toClusterMap()
			n := cluster.Node{
				Address:  m[api.ENDPOINT],
				Region:   m[api.ONEZONE],
				Metadata: m,
				Clusters: c,
			}
			nodes = append(nodes, n)
		}

		//register nodes using the map.
		p.cluster, err = cluster.New(p.storage, nodes...)
		if err != nil {
			return err
		}
	}
	return nil
}

//convert the config to just a map.
func (c Region) toMap() map[string]string {
	m := make(map[string]string)
	m[api.ONEZONE] = c.OneZone
	m[api.ENDPOINT] = c.OneEndPoint
	m[api.USERID] = c.OneUserid
	m[api.PASSWORD] = c.OnePassword
	m[api.TEMPLATE] = c.OneTemplate
	m[api.IMAGE] = c.Image
	m[api.VCPU_PERCENTAGE] = c.VCPUPercentage
	return m
}

func (c Region) toClusterMap() map[string]map[string]string {
	clData := make(map[string]map[string]string)
	for i := 0; i < len(c.Clusters); i++ {
		if c.Clusters[i].Enabled {
			mm, ok := clData[c.Clusters[i].ClusterId]
			if !ok {
				mm = make(map[string]string)
				mm[utils.IPV4PRI] = c.Clusters[i].Vnet_pri_ipv4
				mm[utils.IPV4PUB] = c.Clusters[i].Vnet_pub_ipv4
				mm[utils.IPV6PRI] = c.Clusters[i].Vnet_pri_ipv6
				mm[utils.IPV6PUB] = c.Clusters[i].Vnet_pub_ipv6
				mm[utils.STORAGE_TYPE] = c.Clusters[i].StorageType
				if c.Clusters[i].VOneCloud {
					mm[utils.VONE_CLOUD] = utils.TRUE
				}
				clData[c.Clusters[i].ClusterId] = mm
			}
		}
	}

	return clData
}

func buildClusterStorage() (cluster.Storage, error) {
	return &cluster.MapStorage{}, nil
}

func getRouterForBox(box *provision.Box) (router.Router, error) {
	routerName, err := box.GetRouter()
	if err != nil {
		return nil, err
	}
	return router.Get(routerName)
}

func (p *oneProvisioner) StartupMessage() (string, error) {
	w := new(tabwriter.Writer)
	var b bytes.Buffer
	w.Init(&b, 0, 8, 0, '\t', 0)
	b.Write([]byte(cmd.Colorfy("  > one ", "white", "", "bold") + "\t" +
		cmd.Colorfy(p.String(), "cyan", "", "")))
	fmt.Fprintln(w)
	w.Flush()
	return strings.TrimSpace(b.String()), nil
}

func (p *oneProvisioner) GitDeploy(box *provision.Box, w io.Writer) (string, error) {
	imageId, err := p.gitDeploy(box.Repo, box.ImageVersion, w)
	if err != nil {
		return "", err
	}

	return p.deployPipeline(box, imageId, w)
}

func (p *oneProvisioner) gitDeploy(re *repository.Repo, version string, w io.Writer) (string, error) {
	fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("--- git deploy for box (git:%s)", re.Source)))
	return p.getBuildImage(re, version), nil
}

func (p *oneProvisioner) ImageDeploy(box *provision.Box, imageId string, w io.Writer) (string, error) {
	fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("--- deploy box (%s, image:%s)", box.GetFullName(), imageId)))
	isValid, err := isValidBoxImage(box.GetFullName(), imageId)
	if err != nil {
		return "", err
	}

	if !isValid {
		imageId = p.getBuildImage(box.Repo, box.ImageVersion)
	}
	return p.deployPipeline(box, imageId, w)
}

//start by validating the image.
//1. &updateStatus in Scylla - Deploying..
//2. &create an inmemory machine type from a Box.
//3. &updateStatus in Scylla - Creating..
//4. &followLogs by posting it in the queue.
func (p *oneProvisioner) deployPipeline(box *provision.Box, imageId string, w io.Writer) (string, error) {

	fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("--- deploy box (%s, image:%s)", box.GetFullName(), imageId)))

	actions := []*action.Action{&machCreating}
	if events.IsEnabled(constants.BILLMGR) && !(len(box.QuotaId) > 0) {
		actions = append(actions, &checkBalances, &updateStatusInScylla)
	}
	actions = append(actions, &mileStoneUpdate, &createMachine, &getVmHostIpPort, &mileStoneUpdate, &updateStatusInScylla)
	actions = append(actions, &updateVnchostPostInScylla, &updateStatusInScylla, &setFinalStatus, &updateStatusInScylla, &followLogs)

	pipeline := action.NewPipeline(actions...)

	args := runMachineActionsArgs{
		box:           box,
		imageId:       imageId,
		writer:        w,
		isDeploy:      true,
		machineStatus: constants.StatusLaunching,
		machineState:  constants.StateInitializing,
		provisioner:   p,
	}

	err := pipeline.Execute(args)
	if err != nil {
		fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("--- deploy pipeline for box (%s, image:%s)\n --> %s", box.GetFullName(), imageId, err)))
		return "", err
	}

	fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("--- deploy box (%s, image:%s)OK", box.GetFullName(), imageId)))
	return imageId, nil
}

func (p *oneProvisioner) Destroy(box *provision.Box, w io.Writer) error {

	fmt.Fprintf(w, lb.W(lb.DESTORYING, lb.INFO, fmt.Sprintf("--- destroying box (%s)", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		isDeploy:      false,
		machineStatus: constants.StatusDestroying,
		machineState:  constants.StateDestroying,
		provisioner:   p,
	}

	actions := []*action.Action{
		&machCreating,
		&updateStatusInScylla,
		&mileStoneUpdate,
		&destroyOldMachine,
		&destroyOldRoute,
		&mileStoneUpdate,
		&updateStatusInScylla,
	}

	pipeline := action.NewPipeline(actions...)

	err := pipeline.Execute(args)
	if err != nil {

		fmt.Fprintf(w, lb.W(lb.DESTORYING, lb.ERROR, fmt.Sprintf("--- destroying box (%s)--> %s", box.GetFullName(), err)))
		return err
	}

	fmt.Fprintf(w, lb.W(lb.DESTORYING, lb.INFO, fmt.Sprintf("--- destroying box (%s)OK", box.GetFullName())))
	err = carton.DoneNotify(box, w, alerts.DESTROYED)
	return nil
}

func (p *oneProvisioner) SaveImage(box *provision.Box, w io.Writer) error {
	fmt.Fprintf(w, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("--- creating snapshot box (%s)", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		isDeploy:      false,
		machineStatus: constants.StatusSnapCreating,
		provisioner:   p,
	}

	actions := []*action.Action{
		&machCreating,
		&updateStatusInScylla,
		&createSnapImage,
		&waitUntillImageReady,
		&updateIdInSnapTable,
		&updateStatusInScylla,
	}

	pipeline := action.NewPipeline(actions...)
	err := pipeline.Execute(args)
	if err != nil {
		fmt.Fprintf(w, lb.W(lb.UPDATING, lb.ERROR, fmt.Sprintf("--- creating snapshot box (%s)--> %s", box.GetFullName(), err)))
		return err
	}

	fmt.Fprintf(w, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("--- creating snapshot box (%s)OK", box.GetFullName())))
	return nil
}

func (p *oneProvisioner) DeleteImage(box *provision.Box, w io.Writer) error {
	fmt.Fprintf(w, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("--- removing snapshot box (%s)", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		isDeploy:      false,
		machineStatus: constants.StatusSnapDeleting,
		provisioner:   p,
	}

	actions := []*action.Action{
		&machCreating,
		&updateSnapStatus,
		&updateStatusInScylla,
		&removeSnapShot,
		&updateStatusInScylla,
	}

	pipeline := action.NewPipeline(actions...)
	err := pipeline.Execute(args)
	if err != nil {
		fmt.Fprintf(w, lb.W(lb.UPDATING, lb.ERROR, fmt.Sprintf("--- removing snapshot box (%s)--> %s", box.GetFullName(), err)))
		return err
	}

	fmt.Fprintf(w, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("--- removing snapshot box (%s)OK", box.GetFullName())))
	return nil
}

func (p *oneProvisioner) AttachDisk(box *provision.Box, w io.Writer) error {
	fmt.Fprintf(w, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("--- adding new storage to box (%s)", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		isDeploy:      false,
		machineStatus: constants.StatusDiskAttaching,
		provisioner:   p,
	}

	actions := []*action.Action{
		&machCreating,
		&updateStatusInScylla,
		&addNewStorage,
		&updateIdInDiskTable,
		&updateStatusInScylla,
	}

	pipeline := action.NewPipeline(actions...)
	err := pipeline.Execute(args)
	if err != nil {
		fmt.Fprintf(w, lb.W(lb.UPDATING, lb.ERROR, fmt.Sprintf("--- adding new storage to box (%s)--> %s", box.GetFullName(), err)))
		return err
	}

	fmt.Fprintf(w, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("--- adding new storage to box (%s)OK", box.GetFullName())))
	return nil
}

func (p *oneProvisioner) DetachDisk(box *provision.Box, w io.Writer) error {
	fmt.Fprintf(w, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("--- removing existing storage from box (%s)", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		isDeploy:      false,
		machineStatus: constants.StatusDiskDetaching,
		provisioner:   p,
	}

	actions := []*action.Action{
		&machCreating,
		&updateStatusInScylla,
		&removeDiskStorage,
		&updateStatusInScylla,
	}

	pipeline := action.NewPipeline(actions...)
	err := pipeline.Execute(args)
	if err != nil {
		fmt.Fprintf(w, lb.W(lb.UPDATING, lb.ERROR, fmt.Sprintf("--- removing existing storage from box (%s)--> %s", box.GetFullName(), err)))
		return err
	}

	fmt.Fprintf(w, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("--- removing existing storage from box (%s)OK", box.GetFullName())))
	return nil
}

func (p *oneProvisioner) SetState(box *provision.Box, w io.Writer, changeto utils.Status) error {

	fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("--- stateto %s", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		machineStatus: changeto,
		provisioner:   p,
	}

	stateAction := make([]*action.Action, 0, 4)
	stateAction = append(stateAction, &machCreating, &changeStateofMachine)
	if args.box.PublicIp != "" {
		stateAction = append(stateAction, &updateStatusInScylla, &addNewRoute, &updateStatusInScylla)
	} else {
		stateAction = append(stateAction, &updateStatusInScylla)
	}

	stateAction = append(stateAction, &setFinalState, &updateStatusInScylla)

	actions := stateAction
	pipeline := action.NewPipeline(actions...)

	err := pipeline.Execute(args)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("--- stateto %s OK", box.GetFullName())))
	err = carton.DoneNotify(box, w, alerts.LAUNCHED)
	return err
}

func (p *oneProvisioner) Restart(box *provision.Box, process string, w io.Writer) error {

	fmt.Fprintf(w, lb.W(lb.RESTARTING, lb.INFO, fmt.Sprintf("--- restarting box (%s)", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		isDeploy:      false,
		machineStatus: constants.StatusBootstrapped,
		machineState:  constants.StateRunning,
		provisioner:   p,
	}

	actions := []*action.Action{
		&machCreating,
		&updateStatusInScylla,
		&restartMachine,
		&mileStoneUpdate,
		&updateStatusInScylla,
	}

	pipeline := action.NewPipeline(actions...)

	err := pipeline.Execute(args)
	if err != nil {
		fmt.Fprintf(w, lb.W(lb.RESTARTING, lb.ERROR, fmt.Sprintf("--- restarting box (%s) --> %s", box.GetFullName(), err)))
		return err
	}

	fmt.Fprintf(w, lb.W(lb.RESTARTING, lb.ERROR, fmt.Sprintf("--- restarting box (%s) OK", box.GetFullName())))
	return nil
}

func (p *oneProvisioner) Start(box *provision.Box, process string, w io.Writer) error {

	fmt.Fprintf(w, lb.W(lb.STARTING, lb.INFO, fmt.Sprintf("--- starting box (%s)", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		isDeploy:      false,
		machineStatus: constants.StatusStarting,
		machineState:  constants.StateRunning,
		provisioner:   p,
	}

	actions := []*action.Action{
		&machCreating,
		&updateStatusInScylla,
		&startMachine,
		&mileStoneUpdate,
		&updateStatusInScylla,
	}

	pipeline := action.NewPipeline(actions...)

	err := pipeline.Execute(args)
	if err != nil {

		fmt.Fprintf(w, lb.W(lb.STARTING, lb.ERROR, fmt.Sprintf("--- starting box (%s) -->%s", box.GetFullName(), err)))
		return err
	}

	fmt.Fprintf(w, lb.W(lb.STARTING, lb.INFO, fmt.Sprintf("--- starting box (%s) OK", box.GetFullName())))
	return nil
}

func (p *oneProvisioner) Stop(box *provision.Box, process string, w io.Writer) error {

	fmt.Fprintf(w, lb.W(lb.STOPPING, lb.INFO, fmt.Sprintf("--- stopping box (%s)", box.GetFullName())))
	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		isDeploy:      false,
		machineStatus: constants.StatusStopping,
		machineState:  constants.StateStopped,
		provisioner:   p,
	}
	actions := []*action.Action{
		&machCreating,
		&updateStatusInScylla,
		&stopMachine,
		&mileStoneUpdate,
		&updateStatusInScylla,
	}

	pipeline := action.NewPipeline(actions...)

	err := pipeline.Execute(args)
	if err != nil {

		fmt.Fprintf(w, lb.W(lb.STOPPING, lb.ERROR, fmt.Sprintf("--- stopping box (%s)-->%s", box.GetFullName(), err)))
		return err
	}

	fmt.Fprintf(w, lb.W(lb.STOPPING, lb.INFO, fmt.Sprintf("--- stopping box (%s) OK", box.GetFullName())))
	return nil
}

func (p *oneProvisioner) Shell(provision.ShellOptions) error {
	return provision.ErrNotImplemented
}

func (*oneProvisioner) Addr(box *provision.Box) (string, error) {
	r, err := getRouterForBox(box)
	if err != nil {
		log.Errorf("Failed to get router: %s", err)
		return "", err
	}
	addr, err := r.Addr(box.GetFullName())
	if err != nil {
		log.Errorf("Failed to obtain box %s address: %s", box.GetFullName(), err)
		return "", err
	}
	return addr, nil
}

func (p *oneProvisioner) MetricEnvs(start int64, end int64, region string, w io.Writer) ([]interface{}, error) {

	fmt.Fprintf(w, lb.W(lb.BILLING, lb.INFO, fmt.Sprintf("--- pull metrics for the duration (%d, %d)", start, end)))
	res, err := p.Cluster().Showback(start, end, region)
	if err != nil {

		fmt.Fprintf(w, lb.W(lb.BILLING, lb.ERROR, fmt.Sprintf("--- pull metrics for the duration error(%d, %d)-->%s", start, end)))
		return nil, err
	}

	fmt.Fprintf(w, lb.W(lb.BILLING, lb.INFO, fmt.Sprintf("--- pull metrics for the duration (%d, %d)OK", start, end)))
	return res, nil
}

func (p *oneProvisioner) TriggerBills(account_id, cat_id, name string) error {
	mach := &machine.Machine{
		Name:      name,
		CartonId:  cat_id,
		AccountId: account_id,
	}
	err := mach.Deduct()
	if err != nil {
		return err
	}
	return nil
}

func (p *oneProvisioner) SetBoxStatus(box *provision.Box, w io.Writer, status utils.Status) error {

	fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("--- status %s box %s", box.GetFullName(), status.String())))
	actions := []*action.Action{
		&machCreating,
		&updateStatusInScylla,
	}
	pipeline := action.NewPipeline(actions...)

	args := runMachineActionsArgs{
		box:           box,
		writer:        w,
		machineStatus: status,
		provisioner:   p,
	}

	err := pipeline.Execute(args)
	if err != nil {
		log.Errorf("error on execute status pipeline for box %s - %s", box.GetFullName(), err)
		return err
	}

	fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("--- status %s box %s OK", box.GetFullName(), status.String())))
	return nil
}

func (p *oneProvisioner) SetCName(box *provision.Box, cname string) error {
	r, err := getRouterForBox(box)
	if err != nil {
		return err
	}
	return r.SetCName(cname, box.GetFullName())
}

func (p *oneProvisioner) UnsetCName(box *provision.Box, cname string) error {
	r, err := getRouterForBox(box)
	if err != nil {
		return err
	}
	return r.UnsetCName(cname, box.GetFullName())
}

// PlatformAdd build and push a new template into one
func (p *oneProvisioner) PlatformAdd(name string, args map[string]string, w io.Writer) error {
	return nil
}

func (p *oneProvisioner) PlatformUpdate(name string, args map[string]string, w io.Writer) error {
	return p.PlatformAdd(name, args, w)
}

func (p *oneProvisioner) PlatformRemove(name string) error {
	return nil
}

// getBuildImage returns the image name from box or tosca.
func (p *oneProvisioner) getBuildImage(re *repository.Repo, version string) string {
	if p.usePlatformImage(re) {
		return p.defaultImage
	}
	return re.Gitr() //return the url
}

func (p *oneProvisioner) usePlatformImage(re *repository.Repo) bool {
	return !re.OneClick
}

func (p *oneProvisioner) ExecuteCommandOnce(stdout, stderr io.Writer, box *provision.Box, cmd string, args ...string) error {
	/*if boxs, err := p.listRunnableMachinesByBox(box.GetName()); err ! =nil {
					return err
	    }

		if err := nil; err != nil {
			return err
		}
		if len(boxs) == 0 {
			return provision.ErrBoxNotFound
		}
		box := boxs[0]
		return box.Exec(p, stdout, stderr, cmd, args...)
	*/
	return nil
}
