// Copyright (c) 2017-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package server

import (
	"context"
	"time"

	"github.com/google/go-github/v28/github"
	"github.com/mattermost/mattermost-mattermod/model"
	"github.com/mattermost/mattermost-server/v5/mlog"
	"github.com/pkg/errors"
)

func (s *Server) GetPullRequestFromGithub(pullRequest *github.PullRequest) (*model.PullRequest, error) {
	pr := &model.PullRequest{
		RepoOwner: *pullRequest.Base.Repo.Owner.Login,
		RepoName:  *pullRequest.Base.Repo.Name,
		Number:    *pullRequest.Number,
		Username:  *pullRequest.User.Login,
		FullName:  "",
		Ref:       *pullRequest.Head.Ref,
		Sha:       *pullRequest.Head.SHA,
		State:     *pullRequest.State,
		URL:       *pullRequest.URL,
		CreatedAt: pullRequest.GetCreatedAt(),
	}

	if pullRequest.Head.Repo != nil {
		pr.FullName = *pullRequest.Head.Repo.FullName
	}

	repo, ok := GetRepository(s.Config.Repositories, pr.RepoOwner, pr.RepoName)
	if ok && repo.BuildStatusContext != "" {
		if combined, _, err := s.GithubClient.Repositories.GetCombinedStatus(context.Background(), pr.RepoOwner, pr.RepoName, pr.Sha, nil); err != nil {
			return nil, err
		} else {
			for _, status := range combined.Statuses {
				if *status.Context == repo.BuildStatusContext {
					pr.BuildStatus = *status.State
					pr.BuildLink = *status.TargetURL
					break
				}
			}
		}

		// for the repos using circleci we have the checks now
		if checks, _, err := s.GithubClient.Checks.ListCheckRunsForRef(context.Background(), pr.RepoOwner, pr.RepoName, pr.Sha, nil); err != nil {
			return nil, err
		} else {
			for _, status := range checks.CheckRuns {
				if *status.Name == repo.BuildStatusContext {
					pr.BuildStatus = status.GetStatus()
					pr.BuildConclusion = status.GetConclusion()
					pr.BuildLink = status.GetHTMLURL()
					break
				}
			}
		}
	}

	if labels, _, err := s.GithubClient.Issues.ListLabelsByIssue(context.Background(), pr.RepoOwner, pr.RepoName, pr.Number, nil); err != nil {
		return nil, err
	} else {
		pr.Labels = labelsToStringArray(labels)
	}

	if result := <-s.Store.PullRequest().Save(pr); result.Err != nil {
		mlog.Error(result.Err.Error())
	}

	return pr, nil
}

func (s *Server) GetIssueFromGithub(repoOwner, repoName string, ghIssue *github.Issue) (*model.Issue, error) {
	issue := &model.Issue{
		RepoOwner: repoOwner,
		RepoName:  repoName,
		Number:    *ghIssue.Number,
		Username:  *ghIssue.User.Login,
		State:     *ghIssue.State,
	}

	if labels, _, err := s.GithubClient.Issues.ListLabelsByIssue(context.Background(), issue.RepoOwner, issue.RepoName, issue.Number, nil); err != nil {
		return nil, err
	} else {
		issue.Labels = labelsToStringArray(labels)
	}

	return issue, nil
}

func labelsToStringArray(labels []*github.Label) []string {
	out := make([]string, len(labels))

	for i, label := range labels {
		out[i] = *label.Name
	}

	return out
}

func (s *Server) sendGitHubComment(repoOwner, repoName string, number int, comment string) {
	mlog.Debug("Sending GitHub comment", mlog.Int("issue", number), mlog.String("comment", comment))
	_, _, err := s.GithubClient.Issues.CreateComment(context.Background(), repoOwner, repoName, number, &github.IssueComment{Body: &comment})
	if err != nil {
		mlog.Error("Error commenting", mlog.Err(err))
	}
}

func (s *Server) removeLabel(repoOwner, repoName string, number int, label string) {
	mlog.Info("Removing label on issue", mlog.Int("issue", number), mlog.String("label", label))

	_, err := s.GithubClient.Issues.RemoveLabelForIssue(context.Background(), repoOwner, repoName, number, label)
	if err != nil {
		mlog.Error("Error removing the label", mlog.Err(err))
	}
	mlog.Info("Finished removing the label")
}

func (s *Server) getComments(repoOwner, repoName string, number int) ([]*github.IssueComment, error) {
	comments, _, err := s.GithubClient.Issues.ListComments(context.Background(), repoOwner, repoName, number, nil)
	if err != nil {
		mlog.Error("pr_error", mlog.Err(err))
		return nil, err
	}
	return comments, nil
}

func (s *Server) GetUpdateChecks(owner, repoName string, prNumber int) (*model.PullRequest, error) {
	prGitHub, _, err := s.GithubClient.PullRequests.Get(context.Background(), owner, repoName, prNumber)
	pr, err := s.GetPullRequestFromGithub(prGitHub)
	if err != nil {
		mlog.Error("pr_error", mlog.Err(err))
		return nil, err
	}

	if result := <-s.Store.PullRequest().Save(pr); result.Err != nil {
		mlog.Error(result.Err.Error())
	}

	return pr, nil
}

func (s *Server) getFilenamesInPullRequest(pr *model.PullRequest) ([]string, error) {
	prFiles, _, err := s.GithubClient.PullRequests.ListFiles(context.Background(), pr.RepoOwner, pr.RepoName, pr.Number, nil)
	if err != nil {
		mlog.Error("Error listing the files from a PR", mlog.String("repo", pr.RepoName), mlog.Int("pr", pr.Number), mlog.String("Fullname", pr.FullName), mlog.Err(err))
		return nil, err
	}

	var filenames = make([]string, len(prFiles))
	for _, file := range prFiles {
		filenames = append(filenames, file.GetFilename())
	}

	return filenames, nil
}

func (s *Server) checkUserPermission(user, repoOwner string) bool {
	_, resp, err := s.GithubClient.Organizations.GetOrgMembership(context.Background(), user, repoOwner)
	if resp.StatusCode == 404 {
		mlog.Info("User is not part of the ORG", mlog.String("User", user))
		return false
	}
	if err != nil {
		return false
	}

	return true
}

func (s *Server) isOrgMember(org, user string) (bool, error) {
	isOrgMember, _, err := s.GithubClient.Organizations.IsMember(context.Background(), org, user)
	return isOrgMember, err
}

func (s *Server) checkIfRefExists(pr *model.PullRequest, org string, ref string) (bool, error) {
	_, response, err := s.GithubClient.Git.GetRef(context.Background(), org, pr.RepoName, ref)
	if err != nil {
		return false, err
	}

	if response.StatusCode == 200 {
		mlog.Debug("Reference found. ", mlog.Int("pr", pr.Number), mlog.String("ref", ref))
		return true, nil
	} else if response.StatusCode == 404 {
		mlog.Debug("Unable to find reference. ", mlog.Int("pr", pr.Number), mlog.String("ref", ref))
		return false, nil
	} else {
		mlog.Debug("Unknown response code while trying to check for reference. ", mlog.Int("pr", pr.Number), mlog.Int("response_code", response.StatusCode), mlog.String("ref", ref))
		return false, nil
	}
}

func (s *Server) createRef(pr *model.PullRequest, ref string) {
	_, _, err := s.GithubClient.Git.CreateRef(
		context.Background(),
		pr.RepoOwner,
		pr.RepoName,
		&github.Reference{
			Ref: github.String(ref),
			Object: &github.GitObject{
				SHA: github.String(pr.Sha),
			},
		})

	if err != nil {
		mlog.Error("Error creating reference", mlog.Err(err))
	}
}

func (s *Server) deleteRefWhereCombinedStateEqualsSuccess(repoOwner string, repoName string, ref string) error {
	cStatus, _, _ := s.GithubClient.Repositories.GetCombinedStatus(context.Background(), repoOwner, repoName, ref, nil)
	if cStatus.GetState() == "success" {
		_, err := s.GithubClient.Git.DeleteRef(context.Background(), repoOwner, repoName, ref)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deleteRef(repoOwner string, repoName string, ref string) error {
	_, err := s.GithubClient.Git.DeleteRef(context.Background(), repoOwner, repoName, ref)
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) areChecksSuccessfulForPr(pr *model.PullRequest, org string) (bool, error) {
	mlog.Debug("Checking combined status for ref", mlog.Int("prNumber", pr.Number), mlog.String("ref", pr.Ref), mlog.String("prSha", pr.Sha))
	cStatus, _, err := s.GithubClient.Repositories.GetCombinedStatus(context.Background(), org, pr.RepoName, pr.Sha, nil)
	if err != nil {
		mlog.Err(err)
		return false, err
	}
	mlog.Debug("Retrieved status for pr", mlog.String("status", cStatus.GetState()), mlog.Int("prNumber", pr.Number), mlog.String("prSha", pr.Sha))
	if cStatus.GetState() == "success" || cStatus.GetState() == "pending" || cStatus.GetState() == "" {
		return true, nil
	}
	return false, nil
}

func (s *Server) createRepoStatus(ctx context.Context, pr *model.PullRequest, status *github.RepoStatus) {
	_, _, err := s.GithubClient.Repositories.CreateStatus(ctx, pr.RepoOwner, pr.RepoName, pr.Sha, status)
	if err != nil {
		mlog.Error("Unable to create the github status for for PR", mlog.Int("pr", pr.Number), mlog.Err(err))
		return
	}
}

func (s *Server) waitForStatus(ctx context.Context, pr *model.PullRequest, statusContext string, statusState string) error {
	ticker := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-ctx.Done():
			ticker.Stop()
			return errors.New("timed out waiting for status " + statusContext)
		case <-ticker.C:
			mlog.Debug("Waiting for status", mlog.Int("pr", pr.Number), mlog.String("context", statusContext))
			statuses, _, err := s.GithubClient.Repositories.ListStatuses(context.Background(), pr.RepoOwner, pr.RepoName, pr.Sha, nil)
			if err != nil {
				return err
			}

			hasStatus := false
			for _, status := range statuses {
				if *status.Context == statusContext && *status.State == statusState {
					hasStatus = true
				}
			}

			if !hasStatus {
				continue
			}

			ticker.Stop()
			return nil
		}
	}
}
