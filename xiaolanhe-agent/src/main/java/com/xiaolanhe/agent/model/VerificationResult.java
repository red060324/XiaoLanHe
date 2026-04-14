package com.xiaolanhe.agent.model;

public record VerificationResult(
        boolean passed,
        boolean revised,
        String reason,
        String revisedContent
) {

    public static VerificationResult pass(String reason) {
        return new VerificationResult(true, false, reason, "");
    }
}
