// Copyright (c) 2024 Blacknon. All rights reserved.
// Use of this source code is governed by an MIT license
// that can be found in the LICENSE file.

package monitor

import (
	"fmt"
	"strings"
	"sync"

	sshrun "github.com/blacknon/lssh/ssh"
	mview "github.com/blacknon/mview"
)

type Monitor struct {
	// selected server list
	ServerList []string

	// sshrun.Run
	r *sshrun.Run

	// Node list
	Nodes []*Node

	// View
	View *mview.Application

	// Panel
	PanelCounter int
	Panels       *mview.TabbedPanels

	// BaseTab(List)
	BaseGrid *mview.Grid
	// BaseTop      map[string]*NodeTop
	table        *mview.Table // MainTab(List)'s table
	top          *mview.Grid  // MainTab(List)'s top
	selectedNode string
	enableTop    bool // MainTab(List) enable Top

	sync.Mutex
}

func Run(r *sshrun.Run) (err error) {
	monitor := Monitor{}
	monitor.r = r

	monitor.enableTop = false

	// Create WaitGroup
	wg := sync.WaitGroup{}

	// Create sftp client
	// NOTE: 接続が切れた場合に再接続を行わせるため、一旦エラーチェックなしにする
	for _, server := range r.ServerList {
		wg.Add(1)
		go monitor.CreateNode(server, &wg)
	}

	// Wait for all goroutines to finish
	wg.Wait()

	if len(monitor.Nodes) == 0 {
		err = fmt.Errorf("No server")
		return err
	}

	// Start Monitoring
	for i := range monitor.Nodes {
		go monitor.Nodes[i].StartMonitoring()
		go monitor.Nodes[i].StartMonitoringTasks()
	}

	monitor.StartView()

	return err
}

func (m *Monitor) GetNode(server string) *Node {
	server = strings.TrimSpace(server)

	for i := range m.Nodes {
		if m.Nodes[i].ServerName == server {
			return m.Nodes[i]
		}
	}
	return nil
}

func (m *Monitor) CreateNode(server string, wg *sync.WaitGroup) {
	defer wg.Done()

	// node
	node := NewNode(server)

	m.Lock()
	m.Nodes = append(m.Nodes, node)
	m.Unlock()
}
