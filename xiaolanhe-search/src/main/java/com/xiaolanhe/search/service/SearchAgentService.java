package com.xiaolanhe.search.service;

import com.xiaolanhe.search.model.EvidenceBundle;
import com.xiaolanhe.search.model.SearchAgentRequest;

public interface SearchAgentService {

    EvidenceBundle retrieveEvidence(SearchAgentRequest request);
}
