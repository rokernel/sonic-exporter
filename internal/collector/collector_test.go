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
		sonic_lldp_neighbor_info{local_interface="Ethernet88",local_role="frontpanel",remote_chassis_id="74:86:e2:6d:df:a5",remote_mgmt_ip="192.168.240.123",remote_port_id="hundredGigE1/23",remote_system_name="net-tor-lab001.lau1"} 1
		sonic_lldp_neighbor_info{local_interface="eth0",local_role="management",remote_chassis_id="00:11:22:33:44:55",remote_mgmt_ip="192.168.240.1",remote_port_id="mgmt0",remote_system_name="oob-switch01"} 1
	`

	if err := testutil.CollectAndCompare(lldpCollector, strings.NewReader(neighborMetadata+neighborExpected), "sonic_lldp_neighbor_info"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}
