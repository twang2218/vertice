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
	"errors"
	"fmt"
	"io"
	"io/ioutil"

	log "github.com/Sirupsen/logrus"
	"github.com/megamsys/libgo/action"
	"github.com/megamsys/libgo/events/alerts"
	"github.com/megamsys/libgo/utils"
	constants "github.com/megamsys/libgo/utils"
	vm "github.com/megamsys/opennebula-go/virtualmachine"
	"github.com/megamsys/vertice/carton"
	lb "github.com/megamsys/vertice/logbox"
	"github.com/megamsys/vertice/provision"
	"github.com/megamsys/vertice/provision/one/machine"
)

const (
	START   = "start"
	STOP    = "stop"
	RESTART = "restart"
)

type runMachineActionsArgs struct {
	box           *provision.Box
	writer        io.Writer
	imageId       string
	isDeploy      bool
	machineStatus utils.Status
	machineState  utils.State
	provisioner   *oneProvisioner
}

//If there is a previous machine created and it has a status, we use that.
// eg: if it we have deployed, then make it created after a machine is created in ONE.

var machCreating = action.Action{
	Name: "machine-struct-creating",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" creating struct machine (%s, %s)", args.box.GetFullName(), args.machineStatus.String())))
		mach := machine.Machine{
			Id:           args.box.Id,
			AccountId:    args.box.AccountId,
			CartonId:     args.box.CartonId,
			CartonsId:    args.box.CartonsId,
			Level:        args.box.Level,
			Name:         args.box.GetFullName(),
			Status:       args.machineStatus,
			State:        args.machineState,
			Image:        args.imageId,
			StorageType:  args.box.StorageType,
			Region:       args.box.Region,
			VMId:         args.box.InstanceId,
			VCPUThrottle: args.provisioner.vcpuThrottle,
		}
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" creating struct machine (%s, %s)OK", args.box.GetFullName(), args.machineStatus.String())))
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
	},
}

var updateStatusInScylla = action.Action{
	Name: "update-status-scylla",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}
		mach := ctx.Previous.(machine.Machine)
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" update status for machine (%s, %s)", args.box.GetFullName(), args.machineStatus.String())))
		if err := mach.SetStatus(mach.Status); err != nil {
			return nil, err
		}
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" update status for machine (%s, %s)OK", args.box.GetFullName(), args.machineStatus.String())))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		c := ctx.FWResult.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		w := args.writer
		if w == nil {
			w = ioutil.Discard
		}
		c.SetStatus(constants.StatusPreError)
	},
}

var checkBalances = action.Action{
	Name: "balance-check",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" check balance for user (%s) machine (%s)", args.box.AccountId, args.box.GetFullName())))
		err := mach.CheckCredits(args.box, writer)
		if err != nil {
			_ = mach.SetMileStone(constants.StateMachineParked)
			_ = mach.SetStatus(constants.StatusInsufficientFund)
			return nil, err
		}
		mach.SetStatus(constants.StatusBalanceVerified)
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" check balance for user (%s) machine (%s) OK", args.box.AccountId, args.box.GetFullName())))
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
	},
}

var createMachine = action.Action{
	Name: "create-machine",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}
		err := mach.SetStatus(mach.Status)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" create machine for box (%s, image:%s)/%s", args.box.GetFullName(), args.imageId, args.box.Compute)))
		err = mach.Create(&machine.CreateArgs{
			Box:         args.box,
			Compute:     args.box.Compute,
			Deploy:      true,
			Provisioner: args.provisioner,
		})
		if err != nil {
			mach.SetStatus(constants.StatusPreError)
			_ = carton.DoneNotify(args.box, writer, alerts.FAILURE)
			return nil, err
		}
		mach.State = constants.StateInitialized
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" create machine for box (%s, image:%s)/%s OK", args.box.GetFullName(), args.imageId, args.box.Compute)))
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		c := ctx.FWResult.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		fmt.Println("create machine backward state : ", c.State)
		if c.State != constants.StateInitialized {
			fmt.Println(" backward removing machine")
			err := c.Remove(args.provisioner, args.box.State)
			if err != nil {
				fmt.Fprintf(args.writer, lb.W(lb.DESTORYING, lb.ERROR, fmt.Sprintf("  removing err machine %s", err.Error())))
			}
		}
	},
}

var getVmHostIpPort = action.Action{
	Name: "gethost-port",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}
		err := mach.VmHostIpPort(&machine.CreateArgs{Provisioner: args.provisioner})
		if err != nil {
			_ = carton.DoneNotify(args.box, writer, alerts.FAILURE)
			return nil, err
		}
		mach.Status = constants.StatusVncHostUpdating

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {

	},
}

var updateVnchostPostInScylla = action.Action{
	Name: "update-vnc-host-port",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		err := mach.UpdateVncHostPost()
		if err != nil {
			return nil, err
		}
		mach.Status = constants.StatusVncHostUpdated
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		c := ctx.FWResult.(machine.Machine)
		c.SetStatus(constants.StatusPreError)
	},
}

var setFinalStatus = action.Action{
	Name: "set-final-status",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		mach.Status = constants.StatusVMBooting
		return mach, nil
	},
}

var destroyOldMachine = action.Action{
	Name: "destroy-old-machine",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}

		fmt.Fprintf(writer, lb.W(lb.DESTORYING, lb.INFO, fmt.Sprintf("  destroying old machine %s ----", mach.Name)))
		err := mach.Remove(args.provisioner, args.box.State)
		if err != nil {
			return nil, err
		}

		fmt.Fprintf(writer, lb.W(lb.DESTORYING, lb.INFO, fmt.Sprintf("  destroyed old machine (%s, %s) OK", mach.Id, mach.Name)))
		return ctx.Previous, nil
	},
	Backward: func(ctx action.BWContext) {

	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var startMachine = action.Action{
	Name: "start-machine",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}

		fmt.Fprintf(writer, lb.W(lb.STARTING, lb.INFO, fmt.Sprintf("  starting  machine %s", mach.Name)))
		err := mach.LifecycleOps(args.provisioner, START)
		if err != nil {
			fmt.Fprintf(writer, lb.W(lb.STARTING, lb.ERROR, fmt.Sprintf("  error start machine ( %s)", args.box.GetFullName())))
			return nil, err
		}
		err = mach.WaitUntillVMState(&machine.CreateArgs{Provisioner: args.provisioner}, vm.ACTIVE, vm.RUNNING)
		if err != nil {
			fmt.Fprintf(writer, lb.W(lb.STARTING, lb.ERROR, fmt.Sprintf("  error start machine ( %s)", args.box.GetFullName())))
			return nil, err
		}

		mach.Status = constants.StatusStarted

		fmt.Fprintf(writer, lb.W(lb.STARTING, lb.INFO, fmt.Sprintf("  starting  machine (%s, %s) OK", mach.Id, mach.Name)))
		return mach, nil
	},

	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var stopMachine = action.Action{
	Name: "stop-machine",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}

		fmt.Fprintf(writer, lb.W(lb.STOPPING, lb.INFO, fmt.Sprintf("\n   stopping  machine %s", mach.Name)))
		err := mach.LifecycleOps(args.provisioner, STOP)
		if err != nil {
			fmt.Fprintf(writer, lb.W(lb.STOPPING, lb.ERROR, fmt.Sprintf("  error stop machine ( %s)", args.box.GetFullName())))
			return nil, err
		}
		err = mach.WaitUntillVMState(&machine.CreateArgs{Provisioner: args.provisioner}, vm.POWEROFF, vm.LCM_INIT)
		if err != nil {
			fmt.Fprintf(writer, lb.W(lb.STOPPING, lb.ERROR, fmt.Sprintf("  error stop machine ( %s)", args.box.GetFullName())))
			return nil, err
		}

		mach.Status = constants.StatusStopped

		fmt.Fprintf(writer, lb.W(lb.STOPPING, lb.INFO, fmt.Sprintf("\n   stopping  machine (%s, %s)OK", mach.Id, mach.Name)))
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var restartMachine = action.Action{
	Name: "restart-machine",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}

		fmt.Fprintf(writer, lb.W(lb.RESTARTING, lb.INFO, fmt.Sprintf("restarting  machine %s", mach.Name)))
		err := mach.LifecycleOps(args.provisioner, RESTART)
		if err != nil {
			return nil, err
		}
		mach.Status = constants.StatusRunning

		fmt.Fprintf(writer, lb.W(lb.RESTARTING, lb.INFO, fmt.Sprintf("restarting  machine (%s, %s)OK", mach.Id, mach.Name)))
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var changeStateofMachine = action.Action{
	Name: "change-state-machine",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}

		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("  change state of machine (%s, %s)", args.box.GetFullName(), args.machineStatus.String())))
		mach := machine.Machine{
			Id:       args.box.Id,
			CartonId: args.box.CartonId,
			Level:    args.box.Level,
			Name:     args.box.GetFullName(),
		}
		err := mach.SetStatus(constants.StatusStateupping)
		if err != nil {
			fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("  error change state of machine ( %s)", args.box.GetFullName())))
			return nil, err
		}
		err = mach.ChangeState(args.machineStatus)
		if err != nil {
			fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("  error publish state change of machine ( %s)", args.box.GetFullName())))
			return nil, err
		}
		if args.box.PublicIp != "" {
			mach.Status = constants.StatusNetworkCreating

		} else {
			mach.Status = constants.StatusNetworkSkipped
		}

		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("  change state of machine (%s, %s)OK", args.box.GetFullName(), args.machineStatus.String())))
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		c := ctx.FWResult.(machine.Machine)
		c.SetStatus(constants.StatusError)
	},
}

var addNewRoute = action.Action{
	Name: "add-new-route",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		args := ctx.Params[0].(runMachineActionsArgs)
		mach := ctx.Previous.(machine.Machine)
		r, err := getRouterForBox(args.box)
		if err != nil {
			return mach, err
		}
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}

		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("adding route to machine (%s, %s)", mach.Name, args.box.PublicIp)))
		err = r.SetCName(mach.Name, args.box.PublicIp)
		if err != nil {
			return mach, err
		}
		mach.SetRoutable(args.box.PublicIp)
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("adding route to machine (%s, %s)OK", mach.Name, args.box.PublicIp)))
		mach.Status = constants.StatusNetworkCreated
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		args := ctx.Params[0].(runMachineActionsArgs)
		mach := ctx.FWResult.(machine.Machine)
		r, err := getRouterForBox(args.box)
		w := args.writer
		if w == nil {
			w = ioutil.Discard
		}
		if err != nil {

			fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf(" destroy route error    %s", err.Error())))
		}

		fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("   destroy routes from created machine  (%s, %s)", mach.Id, mach.Name)))
		if mach.Routable {
			err = r.UnsetCName(mach.Name, args.box.PublicIp)
			if err != nil {

				fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("   destroy route error (%s, %s)    %s", mach.Name, args.box.PublicIp, err.Error())))
			}

			fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("destroy route from machine (%s, %s) OK", mach.Id, mach.Name)))
		}
	},
	OnError: rollbackNotice,
}

var destroyOldRoute = action.Action{
	Name: "destroy-old-route",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		args := ctx.Params[0].(runMachineActionsArgs)
		mach := ctx.Previous.(machine.Machine)
		r, err := getRouterForBox(args.box)
		if err != nil {
			return mach, err
		}
		w := args.writer
		if w == nil {
			w = ioutil.Discard
		}
		mach.SetRoutable(args.box.PublicIp)
		fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("destroy routes from created machine")))
		if mach.Routable {
			err = r.UnsetCName(mach.Name, args.box.PublicIp)
			if err != nil {

				fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("destroy route error (%s, %s)   %s", mach.Name, args.box.PublicIp, err.Error())))
			}

			fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("  destroy route from machine (%s, %s)", mach.Name, args.box.PublicIp)))
		} else {

			fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("  skip destroy routes from created machine (%s, %s) OK", mach.Name, args.box.PublicIp)))
		}
		mach.Status = constants.StatusDestroyed
		mach.State = constants.StateDestroyed
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		args := ctx.Params[0].(runMachineActionsArgs)
		mach := ctx.FWResult.(machine.Machine)
		r, err := getRouterForBox(args.box)
		w := args.writer
		if w == nil {
			w = ioutil.Discard
		}
		if err != nil {

			fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("   destroy route error (%s, %s)   %s", mach.Name, args.box.PublicIp, err.Error())))
		}

		fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("  addding back routes to old machine")))
		if mach.Routable {
			err = r.SetCName(mach.Name, args.box.PublicIp)
			if err != nil {

				fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("destroy error (%s, %s)     %s", mach.Name, args.box.PublicIp, err.Error())))
			}

			fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("   adding route to machine (%s, %s) OK", mach.Name, args.box.PublicIp)))
		}
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var followLogs = action.Action{
	Name: "follow-logs",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		c, ok := ctx.Previous.(machine.Machine)
		if !ok {
			return nil, errors.New("Previous result must be a machine.")
		}
		args := ctx.Params[0].(runMachineActionsArgs)
		err := c.Logs(args.provisioner, args.writer)
		if err != nil {
			log.Errorf("error on get logs for machine %s - %s", c.Name, err)
			return nil, err
		}

		return args.imageId, nil
	},
	Backward: func(ctx action.BWContext) {
	},
	MinParams: 1,
}

var rollbackNotice = func(ctx action.FWContext, err error) {
	args := ctx.Params[0].(runMachineActionsArgs)
	if args.writer != nil {

		fmt.Fprintf(args.writer, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("==> ROLLBACK     %s", err)))

	}
}

var createSnapImage = action.Action{
	Name: "create-snapshot-image",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		if writer == nil {
			writer = ioutil.Discard
		}

		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("  creating snapshot machine %s ----", mach.Name)))
		err := mach.CreateDiskSnap(args.provisioner)
		if err != nil {
			return nil, err
		}

		mach.Status = constants.StatusSnapCreated

		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" creating snapshot machine (%s, %s) OK", mach.Id, mach.Name)))
		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		args := ctx.Params[0].(runMachineActionsArgs)
		mach := ctx.FWResult.(machine.Machine)
		mach.Status = constants.Status("error")
		w := args.writer
		if w == nil {
			w = ioutil.Discard
		}
		if err := mach.RemoveSnapshot(args.provisioner); err != nil {
			fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("  snapshot remove failure error (%s)   %s", mach.Name, err.Error())))
		}
		err := mach.UpdateSnapStatus(mach.Status)
		if err != nil {
			fmt.Fprintf(w, lb.W(lb.DEPLOY, lb.ERROR, fmt.Sprintf("  snapshot create failure update error (%s)   %s", mach.Name, err.Error())))
		}
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var removeSnapShot = action.Action{
	Name: "remove-snap-shot",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" remove snapshot for machine (%s, %s)", args.box.GetFullName(), constants.LAUNCHED)))
		if err := mach.RemoveSnapshot(args.provisioner); err != nil {
			return nil, err
		}
		mach.Status = constants.StatusSnapDeleted
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" remove snapshot for machine (%s, %s)OK", args.box.GetFullName(), constants.LAUNCHED)))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var mileStoneUpdate = action.Action{
	Name: "change-milestone-state",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" update milestone state for machine (%s, %s)", args.box.GetFullName(), constants.LAUNCHED)))
		if err := mach.SetMileStone(mach.State); err != nil {
			return nil, err
		}
		fmt.Fprintf(writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf(" update milestone state for machine (%s, %s)OK", args.box.GetFullName(), constants.LAUNCHED)))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		var err error
		c := ctx.FWResult.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		fmt.Fprintf(args.writer, lb.W(lb.DEPLOY, lb.INFO, fmt.Sprintf("\n---- State Changing Backward for %s ----", args.box.GetFullName())))
		err = c.SetMileStone(constants.StatePreError)
		if err != nil {
			log.Errorf("---- [state-change:Backward]\n     %s", err.Error())
		}
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var setFinalState = action.Action{
	Name: "set-final-state",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		mach.Status = constants.StatusStateupped
		return mach, nil
	},
}

var addNewStorage = action.Action{
	Name: "add-new-storage",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("  attaching new disk to machine %s ----", mach.Name)))
		err := mach.AttachNewDisk(args.provisioner)
		if err != nil {
			return nil, err
		}
		mach.Status = constants.StatusDiskAttaching
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf("  attaching new disk to machine (%s, %s) OK", mach.Id, mach.Name)))
		return ctx.Previous, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var updateIdInSnapTable = action.Action{
	Name: "update-snap-table",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" update snapshot status for machine (%s, %s)", args.box.GetFullName(), constants.LAUNCHED)))
		if err := mach.UpdateSnap(); err != nil {
			return nil, err
		}
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" update snapshot status for machine (%s, %s)OK", args.box.GetFullName(), constants.LAUNCHED)))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var updateSnapStatus = action.Action{
	Name: "update-snap-status",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" update snapshot status for machine (%s, %s)", args.box.GetFullName(), constants.LAUNCHED)))
		if err := mach.UpdateSnapStatus(mach.Status); err != nil {
			return nil, err
		}
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" update snapshot status for machine (%s, %s)OK", args.box.GetFullName(), constants.LAUNCHED)))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var waitUntillImageReady = action.Action{
	Name: "wait-for-image-ready",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" waiting to snapshot creating for machine (%s, %s)", args.box.GetFullName(), constants.SNAPSHOTTING)))
		if err := mach.IsSnapReady(args.provisioner); err != nil {
			return nil, err
		}
		mach.Status = constants.StatusRunning
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" waiting to snapshot creating  for machine (%s, %s)OK", args.box.GetFullName(), constants.SNAPSHOTTING)))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var updateIdInDiskTable = action.Action{
	Name: "update-disk-table",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" update disks status for machine (%s, %s)", args.box.GetFullName(), constants.LAUNCHED)))
		if err := mach.UpdateDisk(args.provisioner); err != nil {
			return nil, err
		}
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" update disks status for machine (%s, %s)OK", args.box.GetFullName(), constants.LAUNCHED)))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var removeDiskStorage = action.Action{
	Name: "remove-disk-storage",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" remove disk from machine (%s, %s)", args.box.GetFullName(), constants.LAUNCHED)))
		if err := mach.RemoveDisk(args.provisioner); err != nil {
			return nil, err
		}
		mach.Status = constants.StatusDiskDetached
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" remove disk from machine (%s, %s)OK", args.box.GetFullName(), constants.LAUNCHED)))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}

var quotaUpdate = action.Action{
	Name: "update-quota",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		mach := ctx.Previous.(machine.Machine)
		args := ctx.Params[0].(runMachineActionsArgs)
		writer := args.writer
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" update quota for machine (%s, %s)", args.box.GetFullName(), constants.LAUNCHED)))
		if err := mach.UpdateQuotas(args.box.QuotaId); err != nil {
			return nil, err
		}
		mach.Status = constants.StatusQuotaUpdated
		fmt.Fprintf(writer, lb.W(lb.UPDATING, lb.INFO, fmt.Sprintf(" update quota for machine (%s, %s)OK", args.box.GetFullName(), constants.LAUNCHED)))

		return mach, nil
	},
	Backward: func(ctx action.BWContext) {
		//do you want to add it back.
	},
	OnError:   rollbackNotice,
	MinParams: 1,
}
