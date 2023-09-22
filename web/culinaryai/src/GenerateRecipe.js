import React, { useState } from 'react';
import axios from 'axios';
import { useNavigate } from 'react-router-dom';
import { useLoading } from './LoadingContext';  // Import the useLoading context

const GenerateRecipe = () => {
  const [prompt, setPrompt] = useState('');
  const navigate = useNavigate();
  const { setLoading } = useLoading();  // Get the setLoading function from the context

  const generateRecipe = async () => {
    setLoading(true);  // Set global loading state to true
    try {
      const response = await axios.post('/api/v1/recipes', { userPrompt: prompt });
      if (response.data && response.data.recipe.ID) {
        navigate(`/recipe/${response.data.recipe.ID}`);
      }
    } catch (error) {
      console.error('Error generating recipe:', error);
    } finally {
      setLoading(false);  // Set global loading state to false
    }
  };

  return (
    <div>
      <input
        type="text"
        placeholder="Enter your prompt"
        value={prompt}
        onChange={(e) => setPrompt(e.target.value)}
      />
      <button onClick={generateRecipe}>Generate</button>
    </div>
  );
};

export default GenerateRecipe;
