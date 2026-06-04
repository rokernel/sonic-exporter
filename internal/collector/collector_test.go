package collector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	clientModel "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/promslog"
	"github.com/vinted/sonic-exporter/pkg/redis"
)

func assertMetricFamilyPresence(t *testing.T, c prometheus.Collector, metricName string, wantPresent bool) {
	t.Helper()

	registry := prometheus.NewRegistry()
	registry.MustRegister(c)

	metricFamilies, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	present := false
	for _, mf := range metricFamilies {
		if mf.GetName() == metricName {
			present = true
			break
		}
	}

	if present != wantPresent {
		t.Fatalf("metric family %q presence=%v, want=%v", metricName, present, wantPresent)
	}
}

func getMetricFamily(t *testing.T, c prometheus.Collector, metricName string) *clientModel.MetricFamily {
	t.Helper()

	registry := prometheus.NewRegistry()
	registry.MustRegister(c)

	metricFamilies, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	for _, mf := range metricFamilies {
		if mf.GetName() == metricName {
			return mf
		}
	}

	return nil
}

func hasPsuSlotInMetricFamily(metricFamily *clientModel.MetricFamily, slot string) bool {
	if metricFamily == nil {
		return false
	}

	for _, metric := range metricFamily.Metric {
		for _, label := range metric.Label {
			if label.GetName() == "slot" && label.GetValue() == slot {
				return true
			}
		}
	}

	return false
}

func collectPsuSlotsFromMetricFamily(metricFamily *clientModel.MetricFamily) []string {
	if metricFamily == nil {
		return nil
	}

	seen := make(map[string]struct{})

	for _, metric := range metricFamily.Metric {
		for _, label := range metric.Label {
			if label.GetName() == "slot" {
				seen[label.GetValue()] = struct{}{}
			}
		}
	}

	slots := make([]string, 0, len(seen))
	for slot := range seen {
		slots = append(slots, slot)
	}

	sort.Strings(slots)
	return slots
}

func collectMetricFamilyNamesWithPrefix(t *testing.T, c prometheus.Collector, prefix string) []string {
	t.Helper()

	registry := prometheus.NewRegistry()
	registry.MustRegister(c)

	metricFamilies, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	var names []string
	for _, mf := range metricFamilies {
		name := mf.GetName()
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}

	sort.Strings(names)
	return names
}

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
	os.Setenv("SYSTEM_ENABLED", "true")
	os.Setenv("DOCKER_ENABLED", "true")
	os.Setenv("SYSTEM_COMMAND_ENABLED", "false")
	os.Setenv("SYSTEM_VERSION_FILE", "../../fixtures/test/system_sonic_version.yml")
	os.Setenv("SYSTEM_MACHINE_CONF_FILE", "../../fixtures/test/system_machine.conf")
	os.Setenv("SYSTEM_HOSTNAME_FILE", "../../fixtures/test/system_hostname")
	os.Setenv("SYSTEM_UPTIME_FILE", "../../fixtures/test/system_uptime")
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
	os.Unsetenv("SYSTEM_ENABLED")
	os.Unsetenv("DOCKER_ENABLED")
	os.Unsetenv("SYSTEM_COMMAND_ENABLED")
	os.Unsetenv("SYSTEM_VERSION_FILE")
	os.Unsetenv("SYSTEM_MACHINE_CONF_FILE")
	os.Unsetenv("SYSTEM_HOSTNAME_FILE")
	os.Unsetenv("SYSTEM_UPTIME_FILE")
	os.Exit(exitCode)
}

func TestInterfaceCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	interfaceCollector := NewInterfaceCollector(logger, NewMetricFilter(logger))

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

	hwCollector := NewHwCollector(logger, NewMetricFilter(logger))

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
		# HELP sonic_hw_psu_voltage_volts PSU voltage
		# TYPE sonic_hw_psu_voltage_volts gauge
		# HELP sonic_hw_psu_current_amperes PSU current
		# TYPE sonic_hw_psu_current_amperes gauge
		# HELP sonic_hw_psu_power_watts PSU power
		# TYPE sonic_hw_psu_power_watts gauge
	`

	expected := `

	 sonic_hw_collector_success 1
	 sonic_hw_psu_voltage_volts{slot="1"} 12.4
	 sonic_hw_psu_voltage_volts{slot="2"} 12.3
	 sonic_hw_psu_current_amperes{slot="1"} 5
	 sonic_hw_psu_current_amperes{slot="2"} 5
	 sonic_hw_psu_power_watts{slot="1"} 60
	 sonic_hw_psu_power_watts{slot="2"} 60
	`
	success_metric := "sonic_hw_collector_success"

	if err := testutil.CollectAndCompare(
		hwCollector,
		strings.NewReader(metadata+expected),
		success_metric,
		"sonic_hw_psu_voltage_volts",
		"sonic_hw_psu_current_amperes",
		"sonic_hw_psu_power_watts",
	); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestHwCollectorMetricFilter(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	t.Run("default emits hw metrics", func(t *testing.T) {
		hwCollector := NewHwCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_fan_rpm", true)
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_psu_voltage_volts", true)
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_psu_current_amperes", true)
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_psu_power_watts", true)
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_psu_info", true)

		psuMetricNames := collectMetricFamilyNamesWithPrefix(t, hwCollector, "sonic_hw_psu_")
		expectedPsuMetricNames := []string{
			"sonic_hw_psu_available_status",
			"sonic_hw_psu_current_amperes",
			"sonic_hw_psu_info",
			"sonic_hw_psu_operational_status",
			"sonic_hw_psu_power_watts",
			"sonic_hw_psu_voltage_volts",
		}

		if !reflect.DeepEqual(expectedPsuMetricNames, psuMetricNames) {
			t.Fatalf("unexpected PSU metric families: got %v want %v", psuMetricNames, expectedPsuMetricNames)
		}
	})

	t.Run("wildcard disable removes fan metric families", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_hw_fan_*")
		hwCollector := NewHwCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_fan_rpm", false)
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_fan_operational_status", false)
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_fan_available_status", false)
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_psu_info", true)
	})

	t.Run("exact disable removes hw scrape duration metric", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_hw_scrape_duration_seconds")
		hwCollector := NewHwCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, hwCollector, "sonic_hw_scrape_duration_seconds", false)
	})
}

func TestHwCollectorPsuNumericMetricParsing(t *testing.T) {
	ctx := context.Background()
	redisClient, err := redis.NewClient()
	if err != nil {
		t.Fatalf("failed to create redis client: %v", err)
	}

	err = redisClient.HsetToDb(ctx, "STATE_DB", "PSU_INFO|PSU 3", map[string]string{
		"presence": "true",
		"status":   "true",
		"model":    "BAD-MODEL",
		"serial":   "BAD-SERIAL",
		"voltage":  "",
		"current":  "N/A",
		"power":    "not-a-number",
	})
	if err != nil {
		t.Fatalf("failed to write invalid PSU sample data: %v", err)
	}

	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	hwCollector := NewHwCollector(logger, NewMetricFilter(logger))
	hwCollector.lastScrapeTime = time.Time{}
	hwCollector.cachedMetrics = nil

	voltageFamily := getMetricFamily(t, hwCollector, "sonic_hw_psu_voltage_volts")
	if hasPsuSlotInMetricFamily(voltageFamily, "3") {
		t.Fatalf("unexpected voltage metric for PSU 3 with empty value")
	}

	currentFamily := getMetricFamily(t, hwCollector, "sonic_hw_psu_current_amperes")
	if hasPsuSlotInMetricFamily(currentFamily, "3") {
		t.Fatalf("unexpected current metric for PSU 3 with N/A value")
	}

	powerFamily := getMetricFamily(t, hwCollector, "sonic_hw_psu_power_watts")
	if hasPsuSlotInMetricFamily(powerFamily, "3") {
		t.Fatalf("unexpected power metric for PSU 3 with malformed value")
	}

	if !hasPsuSlotInMetricFamily(voltageFamily, "1") || !hasPsuSlotInMetricFamily(voltageFamily, "2") {
		t.Fatalf("expected valid voltage values from fixture PSUs 1 and 2, got %v", collectPsuSlotsFromMetricFamily(voltageFamily))
	}
}

func TestCrmCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	crmCollector := NewCrmCollector(logger, NewMetricFilter(logger))

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

func TestCrmCollectorMetricFilter(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	t.Run("default emits crm metrics", func(t *testing.T) {
		crmCollector := NewCrmCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, crmCollector, "sonic_crm_resource_used", true)
		assertMetricFamilyPresence(t, crmCollector, "sonic_crm_acl_resource_used", true)
	})

	t.Run("wildcard disable removes resource metric families", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_crm_resource_*")
		crmCollector := NewCrmCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, crmCollector, "sonic_crm_resource_used", false)
		assertMetricFamilyPresence(t, crmCollector, "sonic_crm_resource_available", false)
		assertMetricFamilyPresence(t, crmCollector, "sonic_crm_acl_resource_used", true)
		assertMetricFamilyPresence(t, crmCollector, "sonic_crm_acl_resource_available", true)
	})
}

func TestQueueCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	queueCollector := NewQueueCollector(logger, NewMetricFilter(logger))

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

func TestQueueCollectorMetricFilter(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	t.Run("default emits queue watermark metric", func(t *testing.T) {
		queueCollector := NewQueueCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, queueCollector, "sonic_queue_watermark_bytes_total", true)
	})

	t.Run("exact disable removes queue watermark metric", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_queue_watermark_bytes_total")
		queueCollector := NewQueueCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, queueCollector, "sonic_queue_watermark_bytes_total", false)
	})

	t.Run("wildcard disable removes queue metric families", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_queue_*")
		queueCollector := NewQueueCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, queueCollector, "sonic_queue_watermark_bytes_total", false)
		assertMetricFamilyPresence(t, queueCollector, "sonic_queue_packets_total", false)
		assertMetricFamilyPresence(t, queueCollector, "sonic_queue_collector_success", false)
	})
}

func TestInterfaceCollectorMetricFilter(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	t.Run("default emits interface mtu metric", func(t *testing.T) {
		interfaceCollector := NewInterfaceCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, interfaceCollector, "sonic_interface_mtu_bytes", true)
	})

	t.Run("exact disable removes interface mtu metric", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_interface_mtu_bytes")
		interfaceCollector := NewInterfaceCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, interfaceCollector, "sonic_interface_mtu_bytes", false)
	})
}

func TestLldpCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	lldpCollector := NewLldpCollector(logger, NewMetricFilter(logger))

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

	vlanCollector := NewVlanCollector(logger, NewMetricFilter(logger))

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

func TestVlanCollectorMetricFilter(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	t.Run("wildcard disable removes vlan metric families", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_vlan_*")
		vlanCollector := NewVlanCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, vlanCollector, "sonic_vlan_info", false)
		assertMetricFamilyPresence(t, vlanCollector, "sonic_vlan_members", false)
		assertMetricFamilyPresence(t, vlanCollector, "sonic_vlan_collector_success", false)
	})
}

func TestLagCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	lagCollector := NewLagCollector(logger, NewMetricFilter(logger))

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

	fdbCollector := NewFdbCollector(logger, NewMetricFilter(logger))

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

func TestFdbCollectorMetricFilter(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	t.Run("exact disable removes only fdb entries by port", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_fdb_entries_by_port")
		fdbCollector := NewFdbCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, fdbCollector, "sonic_fdb_entries_by_port", false)
		assertMetricFamilyPresence(t, fdbCollector, "sonic_fdb_entries", true)
	})
}

func TestSystemCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	systemCollector := NewSystemCollector(logger, NewMetricFilter(logger))

	problems, err := testutil.CollectAndLint(systemCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	identityMetadata := `
		# HELP sonic_system_identity_info Switch identity metadata, value is always 1
		# TYPE sonic_system_identity_info gauge
	`

	identityExpected := `
		sonic_system_identity_info{asic="broadcom",asic_count="1",hostname="switch01.example.net",hwsku="Example-SKU-48X",model="Model-X",platform="x86_64-vendor_switch-r0",revision="A01",serial="SN-TEST-0001"} 1
	`

	if err := testutil.CollectAndCompare(systemCollector, strings.NewReader(identityMetadata+identityExpected), "sonic_system_identity_info"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	softwareMetadata := `
		# HELP sonic_system_software_info Switch software metadata, value is always 1
		# TYPE sonic_system_software_info gauge
	`

	softwareExpected := `
		sonic_system_software_info{branch="test-branch",build_commit="abcdef123",build_date="Tue Jan 02 12:34:56 UTC 2024",built_by="ubuntu@sonic-exporter.test",debian_version="10.13",kernel_version="4.19.0-12-2-amd64",release="test-release",sonic_os_version="10",sonic_version="SONiC.SONIC.202012.test"} 1
	`

	if err := testutil.CollectAndCompare(systemCollector, strings.NewReader(softwareMetadata+softwareExpected), "sonic_system_software_info"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	statusMetadata := `
		# HELP sonic_system_collector_success Whether system collector succeeded
		# TYPE sonic_system_collector_success gauge
		# HELP sonic_system_uptime_seconds Switch uptime in seconds
		# TYPE sonic_system_uptime_seconds gauge
	`

	statusExpected := `
		sonic_system_collector_success 1
		sonic_system_uptime_seconds 12345
	`

	if err := testutil.CollectAndCompare(systemCollector, strings.NewReader(statusMetadata+statusExpected), "sonic_system_collector_success", "sonic_system_uptime_seconds"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestSystemCollectorMetricFilter(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	t.Run("exact disable removes uptime metric family only", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_system_uptime_seconds")
		systemCollector := NewSystemCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, systemCollector, "sonic_system_uptime_seconds", false)
		assertMetricFamilyPresence(t, systemCollector, "sonic_system_identity_info", true)
	})
}

func TestDockerCollector(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	dockerCollector := NewDockerCollector(logger, NewMetricFilter(logger))

	problems, err := testutil.CollectAndLint(dockerCollector)
	if err != nil {
		t.Error("metric lint completed with errors")
	}

	for _, problem := range problems {
		t.Errorf("metric %v has a problem: %v", problem.Metric, problem.Text)
	}

	statusMetadata := `
		# HELP sonic_docker_collector_success Whether docker collector succeeded
		# TYPE sonic_docker_collector_success gauge
		# HELP sonic_docker_containers Number of containers with DOCKER_STATS entries
		# TYPE sonic_docker_containers gauge
		# HELP sonic_docker_entries_skipped Number of docker entries skipped during latest refresh
		# TYPE sonic_docker_entries_skipped gauge
		# HELP sonic_docker_source_stale Whether DOCKER_STATS source data is stale (1=yes, 0=no)
		# TYPE sonic_docker_source_stale gauge
	`

	statusExpected := `
		sonic_docker_collector_success 1
		sonic_docker_containers 2
		sonic_docker_entries_skipped 1
		sonic_docker_source_stale 1
	`

	if err := testutil.CollectAndCompare(dockerCollector, strings.NewReader(statusMetadata+statusExpected), "sonic_docker_collector_success", "sonic_docker_containers", "sonic_docker_entries_skipped", "sonic_docker_source_stale"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	containerMetadata := `
		# HELP sonic_docker_container_info Container metadata from SONiC DOCKER_STATS, value is always 1
		# TYPE sonic_docker_container_info gauge
	`

	containerExpected := `
		sonic_docker_container_info{container="swss"} 1
		sonic_docker_container_info{container="syncd"} 1
	`

	if err := testutil.CollectAndCompare(dockerCollector, strings.NewReader(containerMetadata+containerExpected), "sonic_docker_container_info"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}

	cpuMetadata := `
		# HELP sonic_docker_container_cpu_percent Container CPU usage percent
		# TYPE sonic_docker_container_cpu_percent gauge
	`

	cpuExpected := `
		sonic_docker_container_cpu_percent{container="swss"} 1.5
		sonic_docker_container_cpu_percent{container="syncd"} 0.5
	`

	if err := testutil.CollectAndCompare(dockerCollector, strings.NewReader(cpuMetadata+cpuExpected), "sonic_docker_container_cpu_percent"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestDockerCollectorMaxContainers(t *testing.T) {
	t.Setenv("DOCKER_MAX_CONTAINERS", "1")

	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	dockerCollector := NewDockerCollector(logger, NewMetricFilter(logger))

	metadata := `
		# HELP sonic_docker_containers Number of containers with DOCKER_STATS entries
		# TYPE sonic_docker_containers gauge
		# HELP sonic_docker_entries_skipped Number of docker entries skipped during latest refresh
		# TYPE sonic_docker_entries_skipped gauge
	`

	expected := `
		sonic_docker_containers 1
		sonic_docker_entries_skipped 2
	`

	if err := testutil.CollectAndCompare(dockerCollector, strings.NewReader(metadata+expected), "sonic_docker_containers", "sonic_docker_entries_skipped"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestDockerCollectorMetricFilter(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	t.Run("wildcard disable removes docker container metric families", func(t *testing.T) {
		t.Setenv("SONIC_DISABLED_METRICS", "sonic_docker_container_*")
		dockerCollector := NewDockerCollector(logger, NewMetricFilter(logger))
		assertMetricFamilyPresence(t, dockerCollector, "sonic_docker_container_info", false)
		assertMetricFamilyPresence(t, dockerCollector, "sonic_docker_container_cpu_percent", false)
		assertMetricFamilyPresence(t, dockerCollector, "sonic_docker_containers", true)

		metadata := `
			# HELP sonic_docker_entries_skipped Number of docker entries skipped during latest refresh
			# TYPE sonic_docker_entries_skipped gauge
		`
		expected := `
			sonic_docker_entries_skipped 1
		`
		if err := testutil.CollectAndCompare(dockerCollector, strings.NewReader(metadata+expected), "sonic_docker_entries_skipped"); err != nil {
			t.Errorf("unexpected collecting result:\n%s", err)
		}
	})
}

func TestFrrCollectorDisabledByDefault(t *testing.T) {
	promslogConfig := &promslog.Config{}
	logger := promslog.New(promslogConfig)

	frrCollector := NewFrrCollector(logger)

	if frrCollector.IsEnabled() {
		t.Fatal("expected FRR collector to be disabled by default")
	}
}

func TestBuildFrrCollectorArgs(t *testing.T) {
	args := buildFrrCollectorArgs(frrCollectorConfig{
		enabled:                            true,
		socketDirPath:                      "/srv/frr",
		socketTimeout:                      30 * time.Second,
		vtyshEnabled:                       true,
		vtyshPath:                          "/usr/local/bin/vtysh",
		vtyshTimeout:                       45 * time.Second,
		vtyshSudo:                          true,
		vtyshOptions:                       "--vty_socket=/run/frr --config_dir=/etc/frr",
		bgpEnabled:                         true,
		bgp6Enabled:                        true,
		bgpL2VPNEnabled:                    true,
		ospfEnabled:                        true,
		ospfInstances:                      "1,5",
		bfdEnabled:                         false,
		routeEnabled:                       true,
		routeDetailedEnabled:               true,
		rpkiEnabled:                        true,
		vrrpEnabled:                        true,
		pimEnabled:                         true,
		statusEnabled:                      false,
		bgpPeerTypesEnabled:                true,
		bgpPeerTypesKeys:                   "type,role",
		bgpPeerDescriptionsEnabled:         true,
		bgpPeerDescriptionsPlainText:       true,
		bgpPeerGroupsEnabled:               true,
		bgpPeerHostnamesEnabled:            true,
		bgpAdvertisedPrefixesEnabled:       true,
		bgpNextHopInterfaceEnabled:         true,
		bgpMonitoredPrefixesFile:           "/etc/sonic-exporter/prefixes.txt",
		bgpAcceptedFilteredPrefixesEnabled: true,
	})

	joined := strings.Join(args, " ")

	checks := []string{
		"--frr.socket.dir-path=/srv/frr",
		"--frr.socket.timeout=30s",
		"--frr.vtysh",
		"--frr.vtysh.path=/usr/local/bin/vtysh",
		"--frr.vtysh.timeout=45s",
		"--frr.vtysh.sudo",
		"--frr.vtysh.options=--vty_socket=/run/frr --config_dir=/etc/frr",
		"--collector.bgp6",
		"--collector.bgpl2vpn",
		"--collector.ospf.instances=1,5",
		"--no-collector.bfd",
		"--collector.route.detailed-routes",
		"--collector.rpki",
		"--collector.vrrp",
		"--collector.pim",
		"--no-collector.status",
		"--collector.bgp.peer-types",
		"--collector.bgp.peer-types.keys=type",
		"--collector.bgp.peer-types.keys=role",
		"--collector.bgp.peer-descriptions",
		"--collector.bgp.peer-descriptions.plain-text",
		"--collector.bgp.peer-groups",
		"--collector.bgp.peer-hostnames",
		"--collector.bgp.advertised-prefixes",
		"--collector.bgp.next-hop-interface",
		"--collector.bgp.accepted-filtered-prefixes",
		"--collector.bgp.monitored-prefixes=/etc/sonic-exporter/prefixes.txt",
	}

	for _, check := range checks {
		if !strings.Contains(joined, check) {
			t.Fatalf("expected FRR args to contain %q, got %q", check, joined)
		}
	}
}
