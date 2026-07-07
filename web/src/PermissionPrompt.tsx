/**
 * PermissionPrompt — approve/deny for tool permission requests.
 */

import React from 'react';
import type { PermissionRequestedEvent } from './protocol';

interface PermissionPromptProps {
  request: PermissionRequestedEvent;
  onApprove: () => void;
  onDeny: () => void;
}

export function PermissionPrompt({ request, onApprove, onDeny }: PermissionPromptProps) {
  return (
    <div className="permission-prompt">
      <div className="permission-header">permission required</div>
      <div className="permission-detail">
        <strong>tool:</strong> {request.tool_name}
      </div>
      {request.command && (
        <div className="permission-detail">
          <strong>command:</strong> <code>{request.command}</code>
        </div>
      )}
      {request.reason && (
        <div className="permission-detail">
          <strong>reason:</strong> {request.reason}
        </div>
      )}
      <div className="permission-actions">
        <button className="btn btn-deny" onClick={onDeny}>deny</button>
        <button className="btn btn-approve" onClick={onApprove}>approve</button>
      </div>
    </div>
  );
}
