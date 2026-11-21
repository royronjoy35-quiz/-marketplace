package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	AppStatusApplied   = "applied"
	AppStatusAccepted  = "accepted"
	AppStatusSubmitted = "submitted"
	AppStatusCompleted = "completed"
	AppStatusRejected  = "rejected"
	AppStatusRevision  = "revision_requested"
)

type TaskApplication struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	TaskID    primitive.ObjectID `bson:"task_id"`
	WorkerID  int64              `bson:"worker_id"`
	SellerID  int64              `bson:"seller_id"`
	Status    string             `bson:"status"`
	CreatedAt time.Time          `bson:"created_at"`
	UpdatedAt time.Time          `bson:"updated_at"`
}

type TaskSubmission struct {
	ID             primitive.ObjectID `bson:"_id,omitempty"`
	ApplicationID  primitive.ObjectID `bson:"application_id"`
	TaskID         primitive.ObjectID `bson:"task_id"`
	WorkerID       int64              `bson:"worker_id"`
	SellerID       int64              `bson:"seller_id"`
	Proof          string             `bson:"proof"`
	ProofHash      string             `bson:"proof_hash,omitempty"`
	PrecheckStatus string             `bson:"precheck_status,omitempty"`
	Status         string             `bson:"status"`
	ReviewNote     string             `bson:"review_note,omitempty"`
	CreatedAt      time.Time          `bson:"created_at"`
	UpdatedAt      time.Time          `bson:"updated_at"`
}

type TaskFlowStore struct {
	applications *mongo.Collection
	submissions  *mongo.Collection
	sessions     *mongo.Collection
	ledger       *mongo.Collection
	tasks        *mongo.Collection
	users        *mongo.Collection
}

func NewTaskFlowStore(db *Database) *TaskFlowStore {
	return &TaskFlowStore{
		applications: db.DB.Collection("task_applications"),
		submissions:  db.DB.Collection("task_submissions"),
		sessions:     db.DB.Collection("task_sessions"),
		ledger:       db.DB.Collection("earnings_ledger"),
		tasks:        db.DB.Collection("tasks"),
		users:        db.DB.Collection("users"),
	}
}

func (s *TaskFlowStore) Apply(ctx context.Context, taskID primitive.ObjectID, workerID int64) error {
	var task Task
	if err := s.tasks.FindOne(ctx, bson.M{"_id": taskID, "status": TaskStatusActive}).Decode(&task); err != nil {
		return err
	}
	if task.CreatorID == workerID {
		return fmt.Errorf("task creator cannot apply to own task")
	}
	if task.FundingStatus != "funded" {
		return fmt.Errorf("this task is not funded yet; applications are temporarily locked to protect workers")
	}
	var existing TaskApplication
	err := s.applications.FindOne(ctx, bson.M{"task_id": taskID, "worker_id": workerID}).Decode(&existing)
	if err == nil {
		return fmt.Errorf("you have already applied to this task")
	}
	if err != mongo.ErrNoDocuments {
		return err
	}
	accepted, err := s.applications.CountDocuments(ctx, bson.M{"task_id": taskID, "status": bson.M{"$in": []string{AppStatusAccepted, AppStatusSubmitted, AppStatusCompleted, AppStatusRevision}}})
	if err != nil {
		return err
	}
	if accepted >= int64(task.Slots) {
		return fmt.Errorf("all worker slots have been filled")
	}
	now := time.Now().UTC()
	_, err = s.applications.InsertOne(ctx, &TaskApplication{
		TaskID: taskID, WorkerID: workerID, SellerID: task.CreatorID,
		Status: AppStatusApplied, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func (s *TaskFlowStore) ListApplications(ctx context.Context, taskID primitive.ObjectID, sellerID int64) ([]TaskApplication, error) {
	filter := bson.M{"task_id": taskID}
	if sellerID > 0 {
		filter["seller_id"] = sellerID
	}
	cur, err := s.applications.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(50))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []TaskApplication
	err = cur.All(ctx, &out)
	return out, err
}

func (s *TaskFlowStore) SetApplicationStatus(ctx context.Context, appID primitive.ObjectID, sellerID int64, status string) error {
	if status != AppStatusAccepted && status != AppStatusRejected {
		return fmt.Errorf("invalid application action")
	}
	var application TaskApplication
	if err := s.applications.FindOne(ctx, bson.M{"_id": appID, "seller_id": sellerID, "status": AppStatusApplied}).Decode(&application); err != nil {
		return err
	}
	if status == AppStatusAccepted {
		var task Task
		if err := s.tasks.FindOne(ctx, bson.M{"_id": application.TaskID}).Decode(&task); err != nil {
			return err
		}
		accepted, err := s.applications.CountDocuments(ctx, bson.M{"task_id": application.TaskID, "status": bson.M{"$in": []string{AppStatusAccepted, AppStatusSubmitted, AppStatusCompleted, AppStatusRevision}}})
		if err != nil {
			return err
		}
		if accepted >= int64(task.Slots) {
			return fmt.Errorf("all task slots are already filled")
		}
	}
	result, err := s.applications.UpdateOne(ctx,
		bson.M{"_id": appID, "seller_id": sellerID, "status": AppStatusApplied},
		bson.M{"$set": bson.M{"status": status, "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

func (s *TaskFlowStore) SetSubmissionSession(ctx context.Context, workerID int64, appID primitive.ObjectID) error {
	_, err := s.sessions.UpdateOne(ctx, bson.M{"worker_id": workerID},
		bson.M{"$set": bson.M{"application_id": appID, "updated_at": time.Now().UTC()}},
		options.Update().SetUpsert(true))
	return err
}

func (s *TaskFlowStore) GetSubmissionSession(ctx context.Context, workerID int64) (primitive.ObjectID, error) {
	var session struct {
		ApplicationID primitive.ObjectID `bson:"application_id"`
	}
	err := s.sessions.FindOne(ctx, bson.M{"worker_id": workerID}).Decode(&session)
	return session.ApplicationID, err
}

func (s *TaskFlowStore) ClearSubmissionSession(ctx context.Context, workerID int64) error {
	_, err := s.sessions.DeleteOne(ctx, bson.M{"worker_id": workerID})
	return err
}

func (s *TaskFlowStore) Submit(ctx context.Context, workerID int64, appID primitive.ObjectID, proof string) error {
	proof = strings.TrimSpace(proof)
	if len([]rune(proof)) < 5 || len([]rune(proof)) > 3000 {
		return fmt.Errorf("proof must be 5–3000 characters")
	}
	var app TaskApplication
	if err := s.applications.FindOne(ctx, bson.M{"_id": appID, "worker_id": workerID, "status": bson.M{"$in": []string{AppStatusAccepted, AppStatusRevision}}}).Decode(&app); err != nil {
		return fmt.Errorf("application is not currently accepted")
	}
	task, err := s.GetTask(ctx, app.TaskID)
	if err != nil {
		return fmt.Errorf("task could not be loaded")
	}
	decision := s.evaluateSubmission(ctx, workerID, *task, proof)
	now := time.Now().UTC()
	sub := TaskSubmission{
		ID: primitive.NewObjectID(), ApplicationID: app.ID, TaskID: app.TaskID,
		WorkerID: workerID, SellerID: app.SellerID, Proof: proof, ProofHash: decision.ProofHash,
		PrecheckStatus: decision.PrecheckStatus, Status: decision.Status, ReviewNote: strings.Join(decision.Flags, ","),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.submissions.InsertOne(ctx, sub); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("this proof has already been submitted; duplicate evidence was blocked")
		}
		return err
	}
	res, err := s.applications.UpdateOne(ctx,
		bson.M{"_id": appID, "worker_id": workerID, "status": bson.M{"$in": []string{AppStatusAccepted, AppStatusRevision}}},
		bson.M{"$set": bson.M{"status": AppStatusSubmitted, "updated_at": now}},
	)
	if err != nil {
		_, _ = s.submissions.DeleteOne(ctx, bson.M{"_id": sub.ID})
		return err
	}
	if res.MatchedCount == 0 {
		_, _ = s.submissions.DeleteOne(ctx, bson.M{"_id": sub.ID})
		return fmt.Errorf("application changed while the proof was being submitted")
	}
	return nil
}

func (s *TaskFlowStore) ListWorkerApplications(ctx context.Context, workerID int64) ([]TaskApplication, error) {
	cur, err := s.applications.Find(ctx, bson.M{"worker_id": workerID},
		options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(20))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []TaskApplication
	err = cur.All(ctx, &out)
	return out, err
}

func (s *TaskFlowStore) ListSellerSubmissions(ctx context.Context, sellerID int64) ([]TaskSubmission, error) {
	filter := bson.M{"status": bson.M{"$in": []string{"pending_review", "flagged_review"}}}
	if sellerID > 0 {
		filter["seller_id"] = sellerID
	}
	cur, err := s.submissions.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(30))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []TaskSubmission
	err = cur.All(ctx, &out)
	return out, err
}

func (s *TaskFlowStore) ReviewSubmission(ctx context.Context, submissionID primitive.ObjectID, sellerID int64, action, note, reward string) error {
	if action != "approved" && action != "revision_requested" && action != "rejected" {
		return fmt.Errorf("invalid review action")
	}
	var sub TaskSubmission
	if err := s.submissions.FindOne(ctx, bson.M{"_id": submissionID, "seller_id": sellerID, "status": bson.M{"$in": []string{"pending_review", "flagged_review"}}}).Decode(&sub); err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := s.submissions.UpdateOne(ctx, bson.M{"_id": submissionID, "seller_id": sellerID, "status": bson.M{"$in": []string{"pending_review", "flagged_review"}}},
		bson.M{"$set": bson.M{"status": action, "review_note": note, "updated_at": now}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	appStatus := AppStatusRejected
	if action == "approved" {
		appStatus = AppStatusCompleted
	}
	if action == "revision_requested" {
		appStatus = AppStatusRevision
	}
	_, err = s.applications.UpdateOne(ctx, bson.M{"_id": sub.ApplicationID, "worker_id": sub.WorkerID},
		bson.M{"$set": bson.M{"status": appStatus, "updated_at": now}})
	if err != nil {
		return err
	}
	if action == "approved" {
		_, err = s.ledger.UpdateOne(ctx, bson.M{"submission_id": submissionID},
			bson.M{"$setOnInsert": bson.M{
				"submission_id": submissionID, "task_id": sub.TaskID, "worker_id": sub.WorkerID,
				"seller_id": sellerID, "amount_usdt": reward, "status": "pending_funding",
				"created_at": now,
			}}, options.Update().SetUpsert(true))
	}
	return err
}

func (s *TaskFlowStore) GetTask(ctx context.Context, id primitive.ObjectID) (*Task, error) {
	var task Task
	err := s.tasks.FindOne(ctx, bson.M{"_id": id}).Decode(&task)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func taskIDFromCallback(data, prefix string) (primitive.ObjectID, error) {
	value := strings.TrimPrefix(data, prefix)
	return primitive.ObjectIDFromHex(value)
}

func (s *TaskFlowStore) GetApplication(ctx context.Context, appID primitive.ObjectID) (*TaskApplication, error) {
	var app TaskApplication
	err := s.applications.FindOne(ctx, bson.M{"_id": appID}).Decode(&app)
	if err != nil {
		return nil, err
	}
	return &app, nil
}
