package jobserver

import (
	"fmt"
	"time"

	"github.com/pachyderm/pachyderm/src/pkg/uuid"
	"github.com/pachyderm/pachyderm/src/pps"
	"github.com/pachyderm/pachyderm/src/pps/jobserver/run"
	"github.com/pachyderm/pachyderm/src/pps/persist"
	"go.pedge.io/google-protobuf"
	"go.pedge.io/proto/rpclog"
	"go.pedge.io/proto/time"
	"golang.org/x/net/context"
	kube "k8s.io/kubernetes/pkg/client/unversioned"
)

type apiServer struct {
	protorpclog.Logger
	persistAPIClient persist.APIClient
	client           *kube.Client
}

func newAPIServer(
	persistAPIClient persist.APIClient,
	client *kube.Client,
) *apiServer {
	return &apiServer{
		protorpclog.NewLogger("pachyderm.pps.JobAPI"),
		persistAPIClient,
		client,
	}
}

func (a *apiServer) CreateJob(ctx context.Context, request *pps.CreateJobRequest) (response *pps.Job, err error) {
	defer func(start time.Time) { a.Log(request, response, err, time.Since(start)) }(time.Now())
	persistJobInfo := &persist.JobInfo{
		Input:        request.Input,
		OutputParent: request.OutputParent,
	}
	if request.GetTransform() != nil {
		persistJobInfo.Spec = &persist.JobInfo_Transform{
			Transform: request.GetTransform(),
		}
	} else if request.GetPipeline() != nil {
		persistJobInfo.Spec = &persist.JobInfo_PipelineName{
			PipelineName: request.GetPipeline().Name,
		}
	} else {
		return nil, fmt.Errorf("pachyderm.pps.jobserver: both transform and pipeline are not set on %v", request)
	}
	persistJobInfo.JobId = uuid.NewWithoutDashes()
	persistJobInfo.CreatedAt = prototime.TimeToTimestamp(time.Now())
	if err := jobserverrun.StartJob(a.client, persistJobInfo); err != nil {
		return nil, err
	}
	_, err = a.persistAPIClient.CreateJobInfo(ctx, persistJobInfo)
	if err != nil {
		return nil, err
	}
	return &pps.Job{
		Id: persistJobInfo.JobId,
	}, nil
}

func (a *apiServer) InspectJob(ctx context.Context, request *pps.InspectJobRequest) (response *pps.JobInfo, err error) {
	defer func(start time.Time) { a.Log(request, response, err, time.Since(start)) }(time.Now())
	persistJobInfo, err := a.persistAPIClient.GetJobInfo(ctx, request.Job)
	if err != nil {
		return nil, err
	}
	return a.persistJobInfoToJobInfo(ctx, persistJobInfo)
}

func (a *apiServer) ListJob(ctx context.Context, request *pps.ListJobRequest) (response *pps.JobInfos, err error) {
	defer func(start time.Time) { a.Log(request, response, err, time.Since(start)) }(time.Now())
	var persistJobInfos *persist.JobInfos
	persistJobInfos, err = a.persistAPIClient.ListJobInfos(ctx, request)
	if err != nil {
		return nil, err
	}
	jobInfos := make([]*pps.JobInfo, len(persistJobInfos.JobInfo))
	for i, persistJobInfo := range persistJobInfos.JobInfo {
		jobInfo, err := a.persistJobInfoToJobInfo(ctx, persistJobInfo)
		if err != nil {
			return nil, err
		}
		jobInfos[i] = jobInfo
	}
	return &pps.JobInfos{
		JobInfo: jobInfos,
	}, nil
}

func (a *apiServer) GetJobLogs(request *pps.GetJobLogsRequest, responseServer pps.JobAPI_GetJobLogsServer) (err error) {
	// TODO: filter by output stream
	persistJobLogs, err := a.persistAPIClient.GetJobLogs(context.Background(), request.Job)
	if err != nil {
		return err
	}
	for _, persistJobLog := range persistJobLogs.JobLog {
		if request.OutputStream == pps.OutputStream_OUTPUT_STREAM_ALL || persistJobLog.OutputStream == request.OutputStream {
			if err := responseServer.Send(&google_protobuf.BytesValue{Value: persistJobLog.Value}); err != nil {
				return err
			}
		}
	}
	return nil
}

// TODO: bulk get
func (a *apiServer) persistJobInfoToJobInfo(ctx context.Context, persistJobInfo *persist.JobInfo) (*pps.JobInfo, error) {
	job := &pps.Job{Id: persistJobInfo.JobId}
	persistJobStatuses, err := a.persistAPIClient.GetJobStatuses(ctx, job)
	if err != nil {
		return nil, err
	}
	persistJobOutput, err := a.persistAPIClient.GetJobOutput(ctx, job)
	if err != nil {
		return nil, err
	}
	jobInfo := &pps.JobInfo{
		Job:   job,
		Input: persistJobInfo.Input,
	}
	if persistJobInfo.GetTransform() != nil {
		jobInfo.Spec = &pps.JobInfo_Transform{
			Transform: persistJobInfo.GetTransform(),
		}
	}
	if persistJobInfo.GetPipelineName() != "" {
		jobInfo.Spec = &pps.JobInfo_Pipeline{
			Pipeline: &pps.Pipeline{
				Name: persistJobInfo.GetPipelineName(),
			},
		}
	}
	jobInfo.JobStatus = make([]*pps.JobStatus, len(persistJobStatuses.JobStatus))
	for i, persistJobStatus := range persistJobStatuses.JobStatus {
		jobInfo.JobStatus[i] = &pps.JobStatus{
			Type:      persistJobStatus.Type,
			Timestamp: persistJobStatus.Timestamp,
			Message:   persistJobStatus.Message,
		}
	}
	if persistJobOutput != nil {
		jobInfo.Output = persistJobOutput.Output
	}
	return jobInfo, nil
}
