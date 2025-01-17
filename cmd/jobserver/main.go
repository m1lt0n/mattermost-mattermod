// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mattermost/mattermost-mattermod/metrics"
	"github.com/mattermost/mattermost-mattermod/server"
	"github.com/mattermost/mattermost-server/v6/shared/mlog"
	"github.com/robfig/cron/v3"
)

func main() {
	var configFile string
	flag.StringVar(&configFile, "config", "config-mattermod.json", "")
	flag.Parse()

	config, err := server.GetConfig(configFile)
	if err != nil {
		mlog.Error("Unable to load Job Server config", mlog.Err(err), mlog.String("file", configFile))
		os.Exit(1)
	}
	if err = server.SetupLogging(config); err != nil {
		mlog.Error("Unable to configure logging", mlog.Err(err))
		os.Exit(1) // nolint
	}

	// Metrics system
	metricsProvider := metrics.NewPrometheusProvider()
	metricsServer := metrics.NewServer(config.MetricsServerPort, metricsProvider.Handler(), true)
	metricsServer.Start()
	defer metricsServer.Stop()

	mlog.Info("Loaded config", mlog.String("filename", configFile))
	s, err := server.New(config, metricsProvider)
	if err != nil {
		mlog.Error("Unable to start Job Server", mlog.Err(err))
		os.Exit(1) // nolint
	}

	mlog.Info("Starting Job Server")
	s.RefreshMembers()

	c := cron.New()

	_, err = c.AddFunc("0 1 * * *", s.CheckPRActivity)
	if err != nil {
		mlog.Error("failed adding CheckPRActivity cron", mlog.Err(err))
	}

	_, err = c.AddFunc("0 2 * * *", s.RefreshMembers)
	if err != nil {
		mlog.Error("failed adding RefreshMembers cron", mlog.Err(err))
	}

	_, err = c.AddFunc("0 3 * * *", s.CleanOutdatedPRs)
	if err != nil {
		mlog.Error("failed adding CleanOutdatedPRs cron", mlog.Err(err))
	}

	// Run once a month
	_, err = c.AddFunc("0 12 1 * *", s.CleanOutdatedCloudBranches)
	if err != nil {
		mlog.Error("failed adding CleanOutdatedCloudBranches cron", mlog.Err(err))
	}

	_, err = c.AddFunc("@every 30m", func() {
		err2 := s.AutoMergePR()
		if err2 != nil {
			mlog.Error("Error from AutoMergePR", mlog.Err(err2))
		}
	})
	if err != nil {
		mlog.Error("failed adding AutoMergePR cron", mlog.Err(err))
	}

	cronTicker := fmt.Sprintf("@every %dm", s.Config.TickRateMinutes)
	_, err = c.AddFunc(cronTicker, s.Tick)
	if err != nil {
		mlog.Error("failed adding Ticker cron", mlog.Err(err))
	}

	c.Start()

	defer func() {
		mlog.Info("Stopping Job Server")

		c.Stop()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	<-sig
	mlog.Info("Stopped Job Server")
}
