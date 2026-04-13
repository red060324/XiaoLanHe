package com.xiaolanhe.agent.model;

public enum ResponseMode {
    CHAT("chat"),
    QA("qa"),
    GUIDE("guide"),
    COMPARE("compare"),
    RECOMMENDATION("recommendation");

    private final String code;

    ResponseMode(String code) {
        this.code = code;
    }

    public String code() {
        return code;
    }
}
