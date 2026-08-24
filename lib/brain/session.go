package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/eventbus"
	"yufeng/lib/kernel"
	sessionv1 "yufeng/proto/gen/sessionv1"
	"yufeng/proto/gen/sessionv1/sessionv1connect"
)

type sessionAttachmentRecord struct {
	Kind     string `json:"kind"`
	RefID    string `json:"ref_id"`
	ModuleID string `json:"module_id,omitempty"`
}

func decodeSessionAttachments(raw []byte, message *sessionv1.SessionMessage) error {
	if len(raw) == 0 || message == nil {
		return nil
	}
	var records []sessionAttachmentRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return err
	}
	for _, record := range records {
		kind, ok := sessionv1.SessionAttachmentKind_value[record.Kind]
		if !ok {
			kind = int32(sessionv1.SessionAttachmentKind_SESSION_ATTACHMENT_KIND_UNSPECIFIED)
		}
		message.Attachments = append(message.Attachments, &sessionv1.SessionAttachment{
			Kind: sessionv1.SessionAttachmentKind(kind), RefId: record.RefID, ModuleId: record.ModuleID,
		})
	}
	return nil
}

// SessionServer 是人、控制台与贾维斯之间的消息服务。
// SendMessage 在落会话消息后，向 jarvis-1 入队一条 SESSION_MESSAGE 指令。
type SessionServer struct {
	pool     *pgxpool.Pool
	agents   *AgentServer
	jarvisID string
}

// NewSessionServer 构造会话服务。
func NewSessionServer(pool *pgxpool.Pool, agents *AgentServer, jarvisID string) *SessionServer {
	if jarvisID == "" {
		jarvisID = "jarvis-1"
	}
	return &SessionServer{pool: pool, agents: agents, jarvisID: jarvisID}
}

// Handler 返回 Connect 服务端处理器。
func (s *SessionServer) Handler() (string, http.Handler) {
	return sessionv1connect.NewSessionServiceHandler(s, handlerOptions()...)
}

// CreateSession 为当前账户创建一条可审计的人机对话会话。
func (s *SessionServer) CreateSession(ctx context.Context, req *connect.Request[sessionv1.CreateSessionRequest]) (*connect.Response[sessionv1.CreateSessionResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	id, err := newID("ses")
	if err != nil {
		return nil, err
	}
	// owner 取认证身份：会话归属是读权限边界，客户端字段不可信。
	if _, err := s.pool.Exec(ctx, `INSERT INTO sessions(session_id, title, owner) VALUES($1,$2,$3)`, id, req.Msg.Title, user.UserId); err != nil {
		return nil, err
	}
	return connect.NewResponse(&sessionv1.CreateSessionResponse{SessionId: id}), nil
}

// SendMessage 幂等写入用户消息，并排队生成对应的智能代理回复。
func (s *SessionServer) SendMessage(ctx context.Context, req *connect.Request[sessionv1.SendMessageRequest]) (*connect.Response[sessionv1.SendMessageResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if req.Msg.SessionId == "" || req.Msg.Content == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id and content are required"))
	}
	if len(req.Msg.Content) > sessionContentMaxBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("content exceeds 8192 bytes"))
	}
	// 会话消息是外部输入面：当前只允许所有者写入；后续增加协作者时必须在这里显式扩展读写授权。
	var owner string
	err = s.pool.QueryRow(ctx, `SELECT owner FROM sessions WHERE session_id=$1`, req.Msg.SessionId).Scan(&owner)
	if err == pgx.ErrNoRows {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return nil, err
	}
	if owner != user.UserId {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("session belongs to another user"))
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// sender 取认证身份：客户端字段不可信。
	var seq int64
	redacted := RedactSecrets(req.Msg.Content)
	if err := tx.QueryRow(ctx, `INSERT INTO session_messages(session_id, sender, content)
	VALUES($1,$2,$3) RETURNING sequence`, req.Msg.SessionId, user.UserId, redacted).Scan(&seq); err != nil {
		return nil, err
	}
	contentRef := "session:" + req.Msg.SessionId + ":" + strconv.FormatInt(seq, 10)
	_, turnID, err := ensureAgentTurn(ctx, tx, turnSeed{
		SourceKind: threadSourceSession, SourceRef: req.Msg.SessionId, SubjectID: s.jarvisID,
		SourceVersion: seq,
		SourceCursor:  map[string]any{"sessionId": req.Msg.SessionId, "messageSequence": seq},
		InputSnapshot: map[string]any{"sessionId": req.Msg.SessionId, "messageSequence": seq, "contentRef": contentRef},
		BudgetID:      "session:" + req.Msg.SessionId,
		ContentRef:    contentRef,
	})
	if err != nil {
		return nil, err
	}
	if err := s.agents.enqueueInstruction(ctx, tx, s.jarvisID, instructionSession, turnID, sessionInstructionTools, []string{req.Msg.SessionId}); err != nil {
		if !errors.Is(err, errNoAgentKey) {
			return nil, connect.NewError(connect.CodeInternal, errors.New("message stored but agent instruction enqueue failed"))
		}
	}
	if err := writeOutbox(ctx, tx, eventbus.SubjectEvents, "session:"+req.Msg.SessionId+":"+strconv.FormatInt(seq, 10), map[string]any{
		"session_id": req.Msg.SessionId, "sequence": seq,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&sessionv1.SendMessageResponse{MessageSequence: seq}), nil
}

// PollMessages 从游标开始长轮询会话中新到达的消息。
func (s *SessionServer) PollMessages(ctx context.Context, req *connect.Request[sessionv1.PollMessagesRequest]) (*connect.Response[sessionv1.PollMessagesResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireSessionOwner(ctx, s.pool, req.Msg.SessionId, user.UserId); err != nil {
		return nil, err
	}
	wait, err := kernel.ResolveLongPoll(req.Msg.LongPollSeconds, kernel.SessionLongPollDefault, kernel.SessionLongPollMax)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	cursor := messageCursor(req.Msg.Cursor)
	deadline := time.Now().Add(wait)
	for {
		resp, err := s.loadMessagesAfter(ctx, req.Msg.SessionId, cursor)
		if err != nil {
			return nil, err
		}
		if len(resp.Messages) > 0 || time.Now().After(deadline) {
			return connect.NewResponse(resp), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollTick):
		}
	}
}

func (s *SessionServer) loadMessagesAfter(ctx context.Context, sessionID string, cursor int64) (*sessionv1.PollMessagesResponse, error) {
	rows, err := s.pool.Query(ctx, `SELECT sequence, session_id, sender, content, occurred_at, attachments
	FROM session_messages WHERE session_id=$1 AND sequence > $2 ORDER BY sequence`, sessionID, cursor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &sessionv1.PollMessagesResponse{}
	last := cursor
	for rows.Next() {
		var m sessionv1.SessionMessage
		var at time.Time
		var attachmentsRaw []byte
		if err := rows.Scan(&m.Sequence, &m.SessionId, &m.Sender, &m.Content, &at, &attachmentsRaw); err != nil {
			return nil, err
		}
		if err := decodeSessionAttachments(attachmentsRaw, &m); err != nil {
			return nil, err
		}
		m.OccurredAt = timestamppb.New(at)
		resp.Messages = append(resp.Messages, &m)
		last = m.Sequence
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	resp.NextCursor = strconv.FormatInt(last, 10)
	return resp, nil
}

// ListMessages 按游标分页返回当前账户有权读取的会话消息。
func (s *SessionServer) ListMessages(ctx context.Context, req *connect.Request[sessionv1.ListMessagesRequest]) (*connect.Response[sessionv1.ListMessagesResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireSessionOwner(ctx, s.pool, req.Msg.SessionId, user.UserId); err != nil {
		return nil, err
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT sequence, session_id, sender, content, occurred_at, attachments
	FROM session_messages WHERE session_id=$1 ORDER BY sequence DESC LIMIT $2 OFFSET $3`, req.Msg.SessionId, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &sessionv1.ListMessagesResponse{}
	for rows.Next() {
		var m sessionv1.SessionMessage
		var at time.Time
		var attachmentsRaw []byte
		if err := rows.Scan(&m.Sequence, &m.SessionId, &m.Sender, &m.Content, &at, &attachmentsRaw); err != nil {
			return nil, err
		}
		if err := decodeSessionAttachments(attachmentsRaw, &m); err != nil {
			return nil, err
		}
		m.OccurredAt = timestamppb.New(at)
		resp.Messages = append(resp.Messages, &m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Messages) > limit {
		resp.Messages = resp.Messages[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// messageCursor 把会话消息游标还原为序号；空串与非法值都从零起算
// （重放一遍幂等，服务端按 sequence 去重不出错）。
func requireSessionOwner(ctx context.Context, pool *pgxpool.Pool, sessionID, userID string) error {
	var owner string
	err := pool.QueryRow(ctx, `SELECT owner FROM sessions WHERE session_id=$1`, sessionID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return err
	}
	if owner != userID {
		return connect.NewError(connect.CodePermissionDenied, errors.New("session belongs to another user"))
	}
	return nil
}

func messageCursor(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
