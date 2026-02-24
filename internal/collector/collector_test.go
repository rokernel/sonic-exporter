package collector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/promslog"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

type redisDatabase struct {
	DbId string                       `json:"id"`
	Data map[string]map[string]string `json:"data"`
}

func populateRedisData() error {
	var ctx = context.Background()

	files := []string{
		"../../fixtures/test/counters_db_data.json",
		"../../fixtures/test/config_db_data.json",
		"../../fixtures/test/appl_db_data.json",
		"../../fixtures/test/asic_db_data.json",
		"../../fixtures/test/state_db_data.json",
	}

	for _, file := range files {
		err := pushDataFromFile(ctx, file)
		if err != nil {
			return err
		}
	}

	return nil
}

func pushDataFromFile(ctx context.Context, fileName string) error {
	var database redisDatabase

	redisClient, _ := redis.NewClient()

	file, _ := os.Open(fileName)
	defer file.Close()

	byteValue, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	err = json.Unmarshal(byteValue, &database)
	if err != nil {
		return err
	}

	for key, values := range database.Data {
		err := redisClient.HsetToDb(ctx, database.DbId, key, values)
		if err != nil {
			return err
		}
	}

	return nil
}

func TestMain(m *testing.M) {
	s, err := miniredis.Run()
	if err != nil {
		slog.Error("failed to start redis", "error", err)
		os.Exit(1)
	}

	os.Setenv("REDIS_ADDRESS", s.Addr())
	os.Setenv("LLDP_ENABLED", "true")
	os.Setenv("LLDP_INCLUDE_MGMT", "true")
	os.Setenv("VLAN_ENABLED", "true")
	os.Setenv("LAG_ENABLED", "true")
	os.Setenv("FDB_ENABLED", "true")
	err = populateRedisData()
	if err != nil {
		slog.Error("failed to populate redis data", "error", err)
		os.Exit(1)
	}

	exitCode := m.Run()

	s.Close()
	os.Unsetenv("REDIS_ADDRESS")
	os.Unsetenv("LLDP_ENABLED")
	os.Unsetenv("LLDP_INCLUDE_MGMT")
	os.Unsetenv("VLAN_ENABLED")
	os.Unsetenv("LAG_ENABLED")
	os.Unsetenv("FDB_ENABLED")
	os.Exit(exitCode)
}

func TestInterfaceCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	interfaceCollector := NewInterfaceCollector(logger)

	problems, err := testutil.CollectAndLint(interfaceCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	metricCount := testutil.CollectAndCount(interfaceCollector)
	t.Logf("metric count: %v", metricCount)

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	metadata := `
		# HELP sonic_interface_collector_success Whether interface collector succeeded
		# TYPE sonic_interface_collector_success gauge
	`

	expected := `

		sonic_interface_collector_success 1
	`
	success_metric := "sonic_interface_collector_success"

	if err := testutil.CollectAndCompare(interfaceCollector, strings.NewReader(metadata+expected), success_metric); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestHwCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	hwCollector := NewHwCollector(logger)

	problems, err := testutil.CollectAndLint(hwCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	metricCount := testutil.CollectAndCount(hwCollector)
	t.Logf("metric count: %v", metricCount)

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	metadata := `
		# HELP sonic_hw_collector_success Whether hw collector succeeded
		# TYPE sonic_hw_collector_success gauge
	`

	expected := `

	 sonic_hw_collector_success 1
	`
	success_metric := "sonic_hw_collector_success"

	if err := testutil.CollectAndCompare(hwCollector, strings.NewReader(metadata+expected), success_metric); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestCrmCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	crmCollector := NewCrmCollector(logger)

	problems, err := testutil.CollectAndLint(crmCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	metricCount := testutil.CollectAndCount(crmCollector)
	t.Logf("metric count: %v", metricCount)

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	metadata := `
		# HELP sonic_crm_collector_success Whether crm collector succeeded
		# TYPE sonic_crm_collector_success gauge
	`

	expected := `

	 sonic_crm_collector_success 1
	`
	success_metric := "sonic_crm_collector_success"

	if err := testutil.CollectAndCompare(crmCollector, strings.NewReader(metadata+expected), success_metric); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestQueueCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	queueCollector := NewQueueCollector(logger)

	problems, err := testutil.CollectAndLint(queueCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	metricCount := testutil.CollectAndCount(queueCollector)
	t.Logf("metric count: %v", metricCount)

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	metadata := `
		# HELP sonic_queue_collector_success Whether queue collector succeeded
		# TYPE sonic_queue_collector_success gauge
	`

	expected := `

	 sonic_queue_collector_success 1
	`
	success_metric := "sonic_queue_collector_success"

	if err := testutil.CollectAndCompare(queueCollector, strings.NewReader(metadata+expected), success_metric); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestLldpCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	lldpCollector := NewLldpCollector(logger)

	problems, err := testutil.CollectAndLint(lldpCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	metricCount := testutil.CollectAndCount(lldpCollector)
	t.Logf("metric count: %v", metricCount)

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	metadata := `
		# HELP sonic_lldp_collector_success Whether LLDP collector succeeded
		# TYPE sonic_lldp_collector_success gauge
		# HELP sonic_lldp_neighbors Number of LLDP neighbors exported
		# TYPE sonic_lldp_neighbors gauge
	`

	expected := `
		sonic_lldp_collector_success 1
		sonic_lldp_neighbors 2
	`

	if err := testutil.CollectAndCompare(lldpCollector, strings.NewReader(metadata+expected), "sonic_lldp_collector_success", "sonic_lldp_neighbors"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	neighborMetadata := `
		# HELP sonic_lldp_neighbor_info Non-numeric data about LLDP neighbor, value is always 1
		# TYPE sonic_lldp_neighbor_info gauge
	`

	neighborExpected := `
		sonic_lldp_neighbor_info{local_interface="Ethernet88",local_role="frontpanel",remote_chassis_id="74:86:e2:6d:df:a5",remote_mgmt_ip="192.168.240.123",remote_port_desc="Ethernet88",remote_port_display="Ethernet88",remote_port_id="hundredGigE1/23",remote_port_id_subtype="7",remote_system_name="net-tor-lab001.lau1"} 1
		sonic_lldp_neighbor_info{local_interface="eth0",local_role="management",remote_chassis_id="00:11:22:33:44:55",remote_mgmt_ip="192.168.240.1",remote_port_desc="ge-0/0/15.0",remote_port_display="ge-0/0/15.0",remote_port_id="535",remote_port_id_subtype="7",remote_system_name="oob-switch01"} 1
	`

	if err := testutil.CollectAndCompare(lldpCollector, strings.NewReader(neighborMetadata+neighborExpected), "sonic_lldp_neighbor_info"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestVlanCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	vlanCollector := NewVlanCollector(logger)

	problems, err := testutil.CollectAndLint(vlanCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	metadata := `
		# HELP sonic_vlan_collector_success Whether VLAN collector succeeded
		# TYPE sonic_vlan_collector_success gauge
		# HELP sonic_vlan_members Number of VLAN members
		# TYPE sonic_vlan_members gauge
	`

	expected := `
		sonic_vlan_collector_success 1
		sonic_vlan_members{vlan="Vlan1000"} 2
		sonic_vlan_members{vlan="Vlan2000"} 0
	`

	if err := testutil.CollectAndCompare(vlanCollector, strings.NewReader(metadata+expected), "sonic_vlan_collector_success", "sonic_vlan_members"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	memberMetadata := `
		# HELP sonic_vlan_member_info Non-numeric data about VLAN member, value is always 1
		# TYPE sonic_vlan_member_info gauge
	`

	memberExpected := `
		sonic_vlan_member_info{member="Ethernet0",tagging_mode="untagged",vlan="Vlan1000"} 1
		sonic_vlan_member_info{member="PortChannel1",tagging_mode="tagged",vlan="Vlan1000"} 1
	`

	if err := testutil.CollectAndCompare(vlanCollector, strings.NewReader(memberMetadata+memberExpected), "sonic_vlan_member_info"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestLagCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	lagCollector := NewLagCollector(logger)

	problems, err := testutil.CollectAndLint(lagCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	metadata := `
		# HELP sonic_lag_collector_success Whether LAG collector succeeded
		# TYPE sonic_lag_collector_success gauge
		# HELP sonic_lag_members Number of LAG member interfaces
		# TYPE sonic_lag_members gauge
	`

	expected := `
		sonic_lag_collector_success 1
		sonic_lag_members{lag="PortChannel1"} 2
		sonic_lag_members{lag="PortChannel2"} 1
	`

	if err := testutil.CollectAndCompare(lagCollector, strings.NewReader(metadata+expected), "sonic_lag_collector_success", "sonic_lag_members"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	memberMetadata := `
		# HELP sonic_lag_member_status Status of LAG member interface (1=enabled, 0=disabled)
		# TYPE sonic_lag_member_status gauge
	`

	memberExpected := `
		sonic_lag_member_status{lag="PortChannel1",member="Ethernet24"} 1
		sonic_lag_member_status{lag="PortChannel1",member="Ethernet28"} 0
		sonic_lag_member_status{lag="PortChannel2",member="Ethernet92"} 1
	`

	if err := testutil.CollectAndCompare(lagCollector, strings.NewReader(memberMetadata+memberExpected), "sonic_lag_member_status"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestFdbCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	fdbCollector := NewFdbCollector(logger)

	problems, err := testutil.CollectAndLint(fdbCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	metadata := `
		# HELP sonic_fdb_collector_success Whether FDB collector succeeded
		# TYPE sonic_fdb_collector_success gauge
		# HELP sonic_fdb_entries Number of FDB entries
		# TYPE sonic_fdb_entries gauge
		# HELP sonic_fdb_entries_unknown_vlan Number of FDB entries with unknown VLAN mapping
		# TYPE sonic_fdb_entries_unknown_vlan gauge
	`

	expected := `
		sonic_fdb_collector_success 1
		sonic_fdb_entries 4
		sonic_fdb_entries_unknown_vlan 1
	`

	if err := testutil.CollectAndCompare(fdbCollector, strings.NewReader(metadata+expected), "sonic_fdb_collector_success", "sonic_fdb_entries", "sonic_fdb_entries_unknown_vlan"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	portMetadata := `
		# HELP sonic_fdb_entries_by_port Number of FDB entries by port
		# TYPE sonic_fdb_entries_by_port gauge
	`

	portExpected := `
		sonic_fdb_entries_by_port{port="Ethernet0"} 2
		sonic_fdb_entries_by_port{port="Ethernet39"} 2
	`

	if err := testutil.CollectAndCompare(fdbCollector, strings.NewReader(portMetadata+portExpected), "sonic_fdb_entries_by_port"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	vlanMetadata := `
		# HELP sonic_fdb_entries_by_vlan Number of FDB entries by VLAN
		# TYPE sonic_fdb_entries_by_vlan gauge
	`

	vlanExpected := `
		sonic_fdb_entries_by_vlan{vlan="1000"} 2
		sonic_fdb_entries_by_vlan{vlan="2000"} 1
		sonic_fdb_entries_by_vlan{vlan="unknown"} 1
	`

	if err := testutil.CollectAndCompare(fdbCollector, strings.NewReader(vlanMetadata+vlanExpected), "sonic_fdb_entries_by_vlan"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	typeMetadata := `
		# HELP sonic_fdb_entries_by_type Number of FDB entries by entry type
		# TYPE sonic_fdb_entries_by_type gauge
	`

	typeExpected := `
		sonic_fdb_entries_by_type{entry_type="dynamic"} 3
		sonic_fdb_entries_by_type{entry_type="static"} 1
	`

	if err := testutil.CollectAndCompare(fdbCollector, strings.NewReader(typeMetadata+typeExpected), "sonic_fdb_entries_by_type"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	statusMetadata := `
		# HELP sonic_fdb_entries_skipped Number of FDB entries skipped during latest refresh
		# TYPE sonic_fdb_entries_skipped gauge
		# HELP sonic_fdb_entries_truncated Whether FDB collection hit max entries limit (1=yes, 0=no)
		# TYPE sonic_fdb_entries_truncated gauge
	`

	statusExpected := `
		sonic_fdb_entries_skipped 1
		sonic_fdb_entries_truncated 0
	`

	if err := testutil.CollectAndCompare(fdbCollector, strings.NewReader(statusMetadata+statusExpected), "sonic_fdb_entries_skipped", "sonic_fdb_entries_truncated"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}
