// Copyright (c) 2017-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package server

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattermost/mattermost-mattermod/model"
)

func handlePullRequestEvent(event *PullRequestEvent) {
	pr, err := GetPullRequestFromGithub(event.PullRequest)
	if err != nil {
		LogError(err.Error())
		return
	}

	checkPullRequestForChanges(pr)
}

func checkPullRequestForChanges(pr *model.PullRequest) {
	var oldPr *model.PullRequest
	if result := <-Srv.Store.PullRequest().Get(pr.RepoOwner, pr.RepoName, pr.Number); result.Err != nil {
		LogError(result.Err.Error())
		return
	} else if result.Data == nil {
		if result := <-Srv.Store.PullRequest().Save(pr); result.Err != nil {
			LogError(result.Err.Error())
		}

		handlePROpened(pr)

		for _, label := range pr.Labels {
			handlePRLabeled(pr, label)
		}
		return
	} else {
		oldPr = result.Data.(*model.PullRequest)
	}

	prHasChanges := false

	for _, label := range pr.Labels {
		hadLabel := false

		for _, oldLabel := range oldPr.Labels {
			if label == oldLabel {
				hadLabel = true
				break
			}
		}

		if !hadLabel {
			handlePRLabeled(pr, label)
			prHasChanges = true
		}
	}

	for _, oldLabel := range oldPr.Labels {
		hasLabel := false

		for _, label := range pr.Labels {
			if label == oldLabel {
				hasLabel = true
				break
			}
		}

		if !hasLabel {
			handlePRUnlabeled(pr, oldLabel)
			prHasChanges = true
		}
	}

	if oldPr.Ref != pr.Ref {
		prHasChanges = true
	}

	if oldPr.Sha != pr.Sha {
		prHasChanges = true
	}

	if oldPr.BuildStatus != pr.BuildStatus {
		prHasChanges = true
	}

	if oldPr.BuildLink != pr.BuildLink {
		prHasChanges = true
	}

	if prHasChanges {
		LogInfo(fmt.Sprintf("pr %v has changes", pr.Number))
		if result := <-Srv.Store.PullRequest().Save(pr); result.Err != nil {
			LogError(result.Err.Error())
			return
		}
	}
}

func handlePROpened(pr *model.PullRequest) {
	username := pr.Username

	resp, err := http.Get(Config.SignedCLAURL)
	if err != nil {
		LogError("Unable to get CLA list: " + err.Error())
		return
	}

	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		LogError("Unable to read response body: " + err.Error())
		return
	}

	if !strings.Contains(string(body), ">"+username+"<") {
		commentOnIssue(pr.RepoOwner, pr.RepoName, pr.Number, strings.Replace(Config.NeedsToSignCLAMessage, "USERNAME", "@"+username, 1))
	}
}

func handlePRLabeled(pr *model.PullRequest, addedLabel string) {
	LogInfo(fmt.Sprintf("labeled pr %v with %v", pr.Number, addedLabel))

	// Must be sure the comment is created before we let anouther request test
	commentLock.Lock()

	comments, _, err := NewGithubClient().Issues.ListComments(pr.RepoOwner, pr.RepoName, pr.Number, nil)
	if err != nil {
		commentLock.Unlock()
		LogError(fmt.Sprintf("Unable to list comments for PR %v: %v", pr.Number, err.Error()))
		return
	}

	if addedLabel == Config.SetupSpinmintTag && !messageByUserContains(comments, Config.Username, Config.SetupSpinmintMessage) {
		commentOnIssue(pr.RepoOwner, pr.RepoName, pr.Number, Config.SetupSpinmintMessage)
		commentLock.Unlock()

		go waitForBuildAndSetupSpinmint(pr)
	} else {
		defer commentLock.Unlock()

		LogInfo("looking for other labels")

		for _, label := range Config.PrLabels {
			LogInfo("looking for " + label.Label)
			finalMessage := strings.Replace(label.Message, "USERNAME", pr.Username, -1)
			if label.Label == addedLabel && !messageByUserContains(comments, Config.Username, finalMessage) {
				LogInfo("Posted message for label: " + label.Label + " on PR: " + strconv.Itoa(pr.Number))
				commentOnIssue(pr.RepoOwner, pr.RepoName, pr.Number, finalMessage)
			}
		}
	}
}

func checkFileExists(filepath string) bool {
	if _, err := os.Stat(filepath); err == nil {
		return true
	}
	return false
}

func handlePRUnlabeled(pr *model.PullRequest, removedLabel string) {
	commentLock.Lock()

	comments, _, err := NewGithubClient().Issues.ListComments(pr.RepoOwner, pr.RepoName, pr.Number, nil)
	if err != nil {
		commentLock.Unlock()
		LogError(err.Error())
		return
	}

	if removedLabel == Config.SetupSpinmintTag &&
		messageByUserContains(comments, Config.Username, Config.SetupSpinmintMessage) &&
		!messageByUserContains(comments, Config.Username, Config.DestroyedSpinmintMessage) {

		var instanceId string
		for _, comment := range comments {
			if isSpinmintDoneComment(*comment.Body) {
				instanceId = INSTANCE_ID_PATTERN.FindStringSubmatch(*comment.Body)[1]
				break
			}
		}

		if instanceId != "" {
			commentOnIssue(pr.RepoOwner, pr.RepoName, pr.Number, Config.DestroyedSpinmintMessage)
			commentLock.Unlock()

			destroySpinmint(instanceId)
		} else {
			commentLock.Unlock()
		}
	} else {
		commentLock.Unlock()
	}
}

func isSpinmintDoneComment(message string) bool {
	pattern := regexp.MustCompile(strings.Replace(regexp.QuoteMeta(Config.SetupSpinmintDoneMessage), SPINMINT_LINK, ".*", -1))
	return pattern.MatchString(message)
}
