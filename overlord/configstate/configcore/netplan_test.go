// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2021 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package configcore_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "gopkg.in/check.v1"

	"github.com/snapcore/snapd/dbusutil"
	"github.com/snapcore/snapd/dbusutil/netplantest"
	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/overlord/configstate/config"
	"github.com/snapcore/snapd/overlord/configstate/configcore"
	"github.com/snapcore/snapd/overlord/snapstate"
	"github.com/snapcore/snapd/overlord/state"
	"github.com/snapcore/snapd/release"
	"github.com/snapcore/snapd/testutil"
)

type fakeConnectivityCheckStore struct {
	status    map[string]bool
	mockedErr error

	statusSeq []map[string]bool
	seq       int
}

func (sto *fakeConnectivityCheckStore) ConnectivityCheck() (map[string]bool, error) {
	if sto.statusSeq != nil {
		sto.status = sto.statusSeq[sto.seq]
		sto.seq++
	}

	return sto.status, sto.mockedErr
}

var _ = (configcore.ConnectivityCheckStore)(&fakeConnectivityCheckStore{})

type netplanSuite struct {
	configcoreSuite
	testutil.DBusTest

	backend   *netplantest.NetplanServer
	fakestore *fakeConnectivityCheckStore
}

var _ = Suite(&netplanSuite{})

var mockNetplanConfigYaml = `
network:
  renderer: NetworkManager
  version: 2
`

func (s *netplanSuite) SetUpTest(c *C) {
	s.configcoreSuite.SetUpTest(c)
	s.DBusTest.SetUpTest(c)

	backend, err := netplantest.NewNetplanServer(mockNetplanConfigYaml)
	c.Assert(err, IsNil)
	s.AddCleanup(func() { c.Check(backend.Stop(), IsNil) })
	s.backend = backend

	// fake the connectivity store
	s.fakestore = &fakeConnectivityCheckStore{}
	restore := configcore.MockSnapstateStore(func(st *state.State, deviceCtx snapstate.DeviceContext) configcore.ConnectivityCheckStore {
		return s.fakestore
	})
	s.AddCleanup(restore)

	// We mock the system bus with a private session bus, that is
	// good enough for the unit tests. Spread tests cover the real
	// bus.
	restore = dbusutil.MockOnlySystemBusAvailable(s.SessionBus)
	s.AddCleanup(restore)

	restore = release.MockOnClassic(false)
	s.AddCleanup(restore)

	err = os.MkdirAll(filepath.Join(dirs.GlobalRootDir, "/etc/"), 0755)
	c.Assert(err, IsNil)
}

func (s *netplanSuite) TearDownTest(c *C) {
	s.configcoreSuite.TearDownTest(c)
	s.DBusTest.TearDownTest(c)
}

func (s *netplanSuite) TestNetplanGetFromDBusNoSuchService(c *C) {
	s.state.Lock()
	defer s.state.Unlock()

	// Note that we do not export any netplan dbus api here

	tr := config.NewTransaction(s.state)
	netplanCfg := make(map[string]interface{})
	err := tr.Get("core", "system.network.netplan", &netplanCfg)
	c.Assert(err, ErrorMatches, `snap "core" has no "system.network.netplan" configuration option`)
}

func (s *netplanSuite) TestNetplanGetFromDBusNoV2Api(c *C) {
	s.state.Lock()
	defer s.state.Unlock()

	// export the V1 api only (no config support with that)
	s.backend.ExportApiV1()

	tr := config.NewTransaction(s.state)

	// no netplan configuration with the "v1" netplan api
	var str string
	err := tr.Get("core", "system.network.netplan", &str)
	c.Assert(err, ErrorMatches, `snap "core" has no "system.network.netplan" configuration option`)
}

func (s *netplanSuite) TestNetplanGetNoSupportOnClassic(c *C) {
	s.state.Lock()
	defer s.state.Unlock()

	restore := release.MockOnClassic(true)
	s.AddCleanup(restore)

	// export the V2 api, things work with that
	s.backend.ExportApiV2()

	tr := config.NewTransaction(s.state)
	netplanCfg := make(map[string]interface{})
	err := tr.Get("core", "system.network.netplan", &netplanCfg)
	c.Assert(err, ErrorMatches, `snap "core" has no "system.network.netplan" configuration option`)
}

func (s *netplanSuite) TestNetplanGetFromDBusHappy(c *C) {
	s.state.Lock()
	defer s.state.Unlock()

	// export the V2 api, things work with that
	s.backend.ExportApiV2()

	tr := config.NewTransaction(s.state)

	// full doc
	netplanCfg := make(map[string]interface{})
	err := tr.Get("core", "system.network.netplan", &netplanCfg)
	c.Assert(err, IsNil)
	c.Check(netplanCfg, DeepEquals, map[string]interface{}{
		"network": map[string]interface{}{
			"renderer": "NetworkManager",
			"version":  json.Number("2"),
		},
	})
	// the config snapshot is discarded after it was read
	c.Check(s.backend.ConfigApiCancelCalls, Equals, 1)

	// only the "network" subset
	netplanCfg = make(map[string]interface{})
	err = tr.Get("core", "system.network.netplan.network", &netplanCfg)
	c.Assert(err, IsNil)
	c.Check(netplanCfg, DeepEquals, map[string]interface{}{
		"renderer": "NetworkManager",
		"version":  json.Number("2"),
	})
	// the config snapshot is discarded after it was read
	c.Check(s.backend.ConfigApiCancelCalls, Equals, 2)

	// only the "network.version" subset
	var ver json.Number
	err = tr.Get("core", "system.network.netplan.network.version", &ver)
	c.Assert(err, IsNil)
	c.Check(ver, Equals, json.Number("2"))

	// the config snapshot is discarded after it was read
	c.Check(s.backend.ConfigApiCancelCalls, Equals, 3)
	// but nothing was applied
	c.Check(s.backend.ConfigApiTryCalls, Equals, 0)
	c.Check(s.backend.ConfigApiApplyCalls, Equals, 0)
}

func (s *netplanSuite) TestNetplanGetFromDBusNoSuchConfigError(c *C) {
	s.state.Lock()
	defer s.state.Unlock()

	// export the V2 api, things work with that
	s.backend.ExportApiV2()

	tr := config.NewTransaction(s.state)

	// no subkey in in our yaml configuration like that, we get an
	// expected error from the config mechanism
	var str string
	err := tr.Get("core", "system.network.netplan.xxx", &str)
	c.Assert(err, ErrorMatches, `snap "core" has no "system.network.netplan.xxx" configuration option`)
}

func (s *netplanSuite) TestNetplanConnectivityCheck(c *C) {
	tests := []struct {
		status      map[string]bool
		expectedErr string
	}{
		// happy
		{nil, ""},
		{map[string]bool{"host1": true}, ""},
		{map[string]bool{"host1": true, "host2": true}, ""},

		// unhappy
		{map[string]bool{"host1": false}, `cannot connect to "host1"`},
		{map[string]bool{"host1": false, "host2": true}, `cannot connect to "host1"`},
		{map[string]bool{"host1": false, "host2": false}, `cannot connect to "host1,host2"`},
	}

	for _, tc := range tests {
		s.fakestore.status = tc.status
		err := configcore.StoreReachable(s.state)
		if tc.expectedErr != "" {
			c.Check(err, ErrorMatches, tc.expectedErr)
		} else {
			c.Check(err, IsNil)
		}
	}
}

func (s *netplanSuite) TestNetplanWriteConfigHappy(c *C) {
	// export the V2 api, things work with that
	s.backend.ExportApiV2()

	// and everything is fine
	s.fakestore.status = map[string]bool{"host1": true}
	s.backend.ConfigApiTryRet = true
	s.backend.ConfigApiApplyRet = true

	s.state.Lock()
	tr := config.NewTransaction(s.state)
	s.state.Unlock()
	tr.Set("core", "system.network.netplan.network.ethernets.eth0.dhcp4", true)
	tr.Set("core", "system.network.netplan.network.wifi.wlan0.dhcp4", true)

	err := configcore.Run(coreDev, tr)
	c.Assert(err, IsNil)

	c.Check(s.backend.ConfigApiSetCalls, DeepEquals, []string{
		`network=null/90-snapd-conf`,
		`network={"ethernets":{"eth0":{"dhcp4":true}},"renderer":"NetworkManager","version":2,"wifi":{"wlan0":{"dhcp4":true}}}/90-snapd-conf`,
	})
	c.Check(s.backend.ConfigApiTryCalls, Equals, 1)
	c.Check(s.backend.ConfigApiApplyCalls, Equals, 1)
}

func (s *netplanSuite) TestNetplanWriteConfigNoNetworkAfterTry(c *C) {
	// export the V2 api, things work with that
	s.backend.ExportApiV2()

	// we have connectivity but it stops
	s.fakestore.statusSeq = []map[string]bool{
		{"host1": true},
		{"host1": false},
	}
	s.backend.ConfigApiTryRet = true
	s.backend.ConfigApiApplyRet = true

	s.state.Lock()
	tr := config.NewTransaction(s.state)
	s.state.Unlock()
	tr.Set("core", "system.network.netplan.ethernets.eth0.dhcp4", true)

	err := configcore.Run(coreDev, tr)
	c.Assert(err, ErrorMatches, `cannot set netplan config: store no longer reachable`)

	c.Check(s.backend.ConfigApiTryCalls, Equals, 1)
	// one cancel for the initial "Get()" and one after the Try failed
	c.Check(s.backend.ConfigApiCancelCalls, Equals, 2)
	c.Check(s.backend.ConfigApiApplyCalls, Equals, 0)
}

func (s *netplanSuite) TestNetplanWriteConfigCanUnset(c *C) {
	// export the V2 api, things work with that
	s.backend.MockNetplanConfigYaml = `
network:
  renderer: networkd
  version: 2
  bridges:
    br54:
      dhcp4: true
      dhcp6: true`
	s.backend.ExportApiV2()

	// we have connectivity
	s.fakestore.status = map[string]bool{"host1": true}
	s.backend.ConfigApiTryRet = true
	s.backend.ConfigApiApplyRet = true

	// we cannot use mockConf because we need the external config
	// integration from the config.Transaction
	s.state.Lock()
	tr := config.NewTransaction(s.state)
	s.state.Unlock()
	tr.Set("core", "system.network.netplan.network.bridges.br54.dhcp4", nil)

	err := configcore.Run(coreDev, tr)
	c.Assert(err, IsNil)

	c.Check(s.backend.ConfigApiSetCalls, DeepEquals, []string{
		`network=null/90-snapd-conf`,
		`network={"bridges":{"br54":{"dhcp6":true}},"renderer":"networkd","version":2}/90-snapd-conf`,
	})
}

func (s *netplanSuite) TestNetplanNoApplyOnClassic(c *C) {
	restore := release.MockOnClassic(true)
	s.AddCleanup(restore)

	err := configcore.Run(coreDev, &mockConf{
		state: s.state,
		changes: map[string]interface{}{
			"system.network.netplan.network.renderer": "networkd",
		},
	})
	c.Check(err, ErrorMatches, "cannot set netplan configuration on classic")

	err = configcore.Run(coreDev, &mockConf{
		state: s.state,
		changes: map[string]interface{}{
			"system.network.netplan": map[string]interface{}{
				"network": map[string]interface{}{
					"version": 2,
				},
			},
		},
	})
	c.Check(err, ErrorMatches, "cannot set netplan configuration on classic")
}
