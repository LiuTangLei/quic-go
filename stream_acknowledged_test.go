package quic

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitWriteAcknowledgedRequiresPeerACK(t *testing.T) {
	s:=&Stream{sendStr:&SendStream{}}
	ctx,cancel:=context.WithTimeout(t.Context(),time.Second)
	defer cancel()
	done:=make(chan error,1)
	go func(){done<-s.WaitWriteAcknowledged(ctx)}()
	assertWaiting:=func(){
		t.Helper()
		select { case err:=<-done:t.Fatalf("returned before ACK: %v",err);case <-time.After(5*time.Millisecond): }
	}
	assertWaiting()
	s.sendStr.mutex.Lock();s.sendStr.finishedWriting=true;s.sendStr.mutex.Unlock()
	assertWaiting()
	s.sendStr.mutex.Lock();s.sendStr.finSent=true;s.sendStr.mutex.Unlock()
	assertWaiting()
	s.sendStr.mutex.Lock();s.sendStr.completed=true;s.sendStr.mutex.Unlock()
	if err:=<-done;err!=nil {t.Fatal(err)}
}

func TestWaitWriteAcknowledgedCancellationAndReset(t *testing.T) {
	s:=&Stream{sendStr:&SendStream{}}
	ctx,cancel:=context.WithCancel(t.Context());cancel()
	if err:=s.WaitWriteAcknowledged(ctx);!errors.Is(err,context.Canceled){t.Fatalf("cancel: %v",err)}
	want:=errors.New("connection closed before ACK")
	s.sendStr.shutdownErr=want
	if err:=s.WaitWriteAcknowledged(t.Context());!errors.Is(err,want){t.Fatalf("shutdown: %v",err)}
	s.sendStr.shutdownErr=nil
	s.sendStr.completed,s.sendStr.finSent=true,true
	s.sendStr.resetErr=&StreamError{ErrorCode:42}
	if err:=s.WaitWriteAcknowledged(t.Context());err==nil {t.Fatal("reset stream claimed acknowledgment")}
}
