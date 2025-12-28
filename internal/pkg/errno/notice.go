package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

var (
	// ErrNoticeChannelNotFound indicates notice channel does not exist.
	ErrNoticeChannelNotFound = errorsx.New(http.StatusNotFound, "NotFound.NoticeChannelNotFound", "The requested notice channel was not found.")
	// ErrNoticeChannelCreateFailed indicates notice channel create failure.
	ErrNoticeChannelCreateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeChannelCreateFailed", "Failed to create the notice channel.")
	// ErrNoticeChannelUpdateFailed indicates notice channel update failure.
	ErrNoticeChannelUpdateFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeChannelUpdateFailed", "Failed to update the notice channel.")
	// ErrNoticeChannelDeleteFailed indicates notice channel soft-delete(disable) failure.
	ErrNoticeChannelDeleteFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeChannelDeleteFailed", "Failed to disable the notice channel.")
	// ErrNoticeChannelGetFailed indicates notice channel get failure.
	ErrNoticeChannelGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeChannelGetFailed", "Failed to retrieve the notice channel.")
	// ErrNoticeChannelListFailed indicates notice channel list failure.
	ErrNoticeChannelListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeChannelListFailed", "Failed to list notice channels.")

	// ErrNoticeDeliveryNotFound indicates notice delivery does not exist.
	ErrNoticeDeliveryNotFound = errorsx.New(http.StatusNotFound, "NotFound.NoticeDeliveryNotFound", "The requested notice delivery was not found.")
	// ErrNoticeDeliveryGetFailed indicates notice delivery get failure.
	ErrNoticeDeliveryGetFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeDeliveryGetFailed", "Failed to retrieve the notice delivery.")
	// ErrNoticeDeliveryListFailed indicates notice delivery list failure.
	ErrNoticeDeliveryListFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeDeliveryListFailed", "Failed to list notice deliveries.")
	// ErrNoticeDeliveryReplayFailed indicates notice delivery replay failure.
	ErrNoticeDeliveryReplayFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeDeliveryReplayFailed", "Failed to replay the notice delivery.")
	// ErrNoticeDeliveryReplayLatestChannelNotFound indicates replay(use_latest_channel=1) cannot refresh snapshot due missing channel.
	ErrNoticeDeliveryReplayLatestChannelNotFound = errorsx.New(http.StatusConflict, "Conflict.NoticeDeliveryReplayLatestChannelNotFound", "Replay with latest channel requires an existing channel.")
	// ErrNoticeDeliveryCancelFailed indicates notice delivery cancel failure.
	ErrNoticeDeliveryCancelFailed = errorsx.New(http.StatusInternalServerError, "InternalError.NoticeDeliveryCancelFailed", "Failed to cancel the notice delivery.")
)
